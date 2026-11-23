package edi

// This file implements X12 envelope (interchange/group/transaction) control number tracking,
// validation and per-record identity emission.
//
// Background:
//   - X12 envelopes repeat control numbers in their header and trailer segments:
//       interchange: ISA13 == IEA02
//       group:       GS06  == GE02
//       transaction: ST02  == SE02
//     If those don't pair up, the envelope is corrupt and the transaction can't be reliably
//     identified downstream.
//   - Suppliers sometimes reuse an interchange control number (ISA13) for different deliveries
//     (different ISA09/ISA10 or GS04/GS05 date/time). Downstream de-duplication keyed merely on
//     the interchange control number would then swallow genuinely different transactions. To
//     prevent that, every target record inside a transaction set is given a stable identity,
//     emitted as a synthetic '__envelope' element (very much like the csv readers' '__debug'
//     element):
//       '__envelope/partner_id'     - stable trading partner identity (ISA05/06/07/08)
//       '__envelope/transaction_id' - stable transaction identity: interchange control number
//                                     + delivery date/time, group control number + date/time and
//                                     transaction set type + control number.
//     Because the element is part of the raw record's IDR node, RawRecord.Checksum() (a hash of
//     idr.JSONify2(node)) also changes when the envelope identity changes, so target
//     de-duplication won't collapse two genuinely different transactions.
//
// Both behaviors can be disabled through the 'envelope' file_declaration setting. Only X12
// envelope segment names (ISA/IEA/GS/GE/ST/SE) are recognized; other EDI flavors (e.g.
// EDIFACT's UNB/UNZ, UNH/UNT) are untouched.

import (
	"fmt"
	"strings"

	"github.com/jf-tech/go-corelib/strs"

	"github.com/jf-tech/omniparser/idr"
)

const (
	segNameISA = "ISA"
	segNameIEA = "IEA"
	segNameGS  = "GS"
	segNameGE  = "GE"
	segNameST  = "ST"
	segNameSE  = "SE"
)

// 1-based element indexes (element 1 is the first element after the segment name), per X12.
const (
	elemISA05 = 5  // Interchange Sender ID Qualifier
	elemISA06 = 6  // Interchange Sender ID
	elemISA07 = 7  // Interchange Receiver ID Qualifier
	elemISA08 = 8  // Interchange Receiver ID
	elemISA09 = 9  // Interchange Date (YYMMDD)
	elemISA10 = 10 // Interchange Time (HHMM)
	elemISA13 = 13 // Interchange Control Number

	elemIEA02 = 2 // Interchange Control Number

	elemGS04 = 4 // Date (CCYYMMDD)
	elemGS05 = 5 // Time (HHMM)
	elemGS06 = 6 // Group Control Number

	elemGE02 = 2 // Group Control Number

	elemST01 = 1 // Transaction Set Identifier Code
	elemST02 = 2 // Transaction Set Control Number

	elemSE02 = 2 // Transaction Set Control Number
)

// Names of the synthetic identity element and its children.
const (
	envelopeIdentityElem      = "__envelope"
	envelopePartnerIDElem     = "partner_id"
	envelopeTransactionIDElem = "transaction_id"
	envelopePartnerElem       = "partner"
	envelopeSenderQualifier   = "sender_qualifier"
	envelopeSenderID          = "sender_id"
	envelopeReceiverQualifier = "receiver_qualifier"
	envelopeReceiverID        = "receiver_id"
	envelopeInterchangeElem   = "interchange"
	envelopeGroupElem         = "group"
	envelopeTransactionElem   = "transaction"
	envelopeControlNumber     = "control_number"
	envelopeDate              = "date"
	envelopeTime              = "time"
	envelopeType              = "type"
)

// envelopeEnd captures one end (header or trailer) of a control number pairing: its value and
// where in the input it appeared, so a mismatch can report both ends.
type envelopeEnd struct {
	segName            string
	value              string
	present            bool
	segCount           int
	runeBegin, runeEnd int
}

func (e envelopeEnd) String() string {
	if !e.present {
		return fmt.Sprintf("segment '%s' has no control number", e.segName)
	}
	return fmt.Sprintf("segment '%s' at segment no.%d (char[%d,%d]) has control number '%s'",
		e.segName, e.segCount, e.runeBegin, e.runeEnd, e.value)
}

// envelopeIdentity is a snapshot of envelope identity in effect at the point a target record
// starts. It is embedded into the target's IDR node when the target is emitted.
type envelopeIdentity struct {
	partnerID     string
	transactionID string

	senderQualifier   string
	senderID          string
	receiverQualifier string
	receiverID        string

	isaCtl  string
	isaDate string
	isaTime string

	gsCtl  string
	gsDate string
	gsTime string

	stType string
	stCtl  string
}

// addTo appends the synthetic '__envelope' element to a target node.
func (id envelopeIdentity) addTo(target *idr.Node) {
	if target == nil {
		return
	}
	env := idr.CreateNode(idr.ElementNode, envelopeIdentityElem)
	addTextChild(env, envelopePartnerIDElem, id.partnerID)
	addTextChild(env, envelopeTransactionIDElem, id.transactionID)

	partner := idr.CreateNode(idr.ElementNode, envelopePartnerElem)
	addTextChild(partner, envelopeSenderQualifier, id.senderQualifier)
	addTextChild(partner, envelopeSenderID, id.senderID)
	addTextChild(partner, envelopeReceiverQualifier, id.receiverQualifier)
	addTextChild(partner, envelopeReceiverID, id.receiverID)
	idr.AddChild(env, partner)

	interchange := idr.CreateNode(idr.ElementNode, envelopeInterchangeElem)
	addTextChild(interchange, envelopeControlNumber, id.isaCtl)
	addTextChild(interchange, envelopeDate, id.isaDate)
	addTextChild(interchange, envelopeTime, id.isaTime)
	idr.AddChild(env, interchange)

	group := idr.CreateNode(idr.ElementNode, envelopeGroupElem)
	addTextChild(group, envelopeControlNumber, id.gsCtl)
	addTextChild(group, envelopeDate, id.gsDate)
	addTextChild(group, envelopeTime, id.gsTime)
	idr.AddChild(env, group)

	transaction := idr.CreateNode(idr.ElementNode, envelopeTransactionElem)
	addTextChild(transaction, envelopeType, id.stType)
	addTextChild(transaction, envelopeControlNumber, id.stCtl)
	idr.AddChild(env, transaction)

	idr.AddChild(target, env)
}

// envelopeTracker tracks the current X12 envelope while the reader streams through segments.
type envelopeTracker struct {
	validate       bool
	outputIdentity bool
	releaseChar    []byte

	isaSeen bool // an ISA segment has been seen for the current interchange
	stSeen  bool // a transaction set (ST) is currently open; cleared by SE/IEA

	isaHeader envelopeEnd
	gsHeader  envelopeEnd
	stHeader  envelopeEnd

	// Identity of the current interchange/group/transaction, captured from header segments.
	senderQualifier   string
	senderID          string
	receiverQualifier string
	receiverID        string
	isaCtl            string
	isaDate           string
	isaTime           string
	gsCtl             string
	gsDate            string
	gsTime            string
	stType            string
	stCtl             string
}

func newEnvelopeTracker(decl *EnvelopeDecl, releaseChar []byte) envelopeTracker {
	return envelopeTracker{
		validate:       decl.validateControlCodes(),
		outputIdentity: decl.outputIdentity(),
		releaseChar:    releaseChar,
	}
}

func (t *envelopeTracker) elemValue(rawSeg RawSeg, elemIndex int) (string, bool) {
	for _, e := range rawSeg.Elems {
		if e.ElemIndex == elemIndex && e.CompIndex == 1 {
			return strings.TrimSpace(string(strs.ByteUnescape(e.Data, t.releaseChar, false))), true
		}
	}
	return "", false
}

func (t *envelopeTracker) end(rawSeg RawSeg, segName string, elemIndex int, segCount, runeBegin, runeEnd int) envelopeEnd {
	v, ok := t.elemValue(rawSeg, elemIndex)
	return envelopeEnd{
		segName:   segName,
		value:     v,
		present:   ok && v != "",
		segCount:  segCount,
		runeBegin: runeBegin,
		runeEnd:   runeEnd,
	}
}

// track processes one raw segment. If the segment closes an envelope level whose control number
// doesn't pair with the one seen in the corresponding header segment, a non-nil error is
// returned; the error message reports both ends.
func (t *envelopeTracker) track(rawSeg RawSeg, segCount, runeBegin, runeEnd int) error {
	if !t.validate && !t.outputIdentity {
		return nil
	}
	switch rawSeg.Name {
	case segNameISA:
		// A new interchange starts. Reset all lower level contexts so no stale values leak
		// into the new interchange.
		t.isaSeen = true
		t.stSeen = false
		t.gsHeader = envelopeEnd{}
		t.stHeader = envelopeEnd{}
		t.senderQualifier, _ = t.elemValue(rawSeg, elemISA05)
		t.senderID, _ = t.elemValue(rawSeg, elemISA06)
		t.receiverQualifier, _ = t.elemValue(rawSeg, elemISA07)
		t.receiverID, _ = t.elemValue(rawSeg, elemISA08)
		t.isaDate, _ = t.elemValue(rawSeg, elemISA09)
		t.isaTime, _ = t.elemValue(rawSeg, elemISA10)
		t.isaHeader = t.end(rawSeg, segNameISA, elemISA13, segCount, runeBegin, runeEnd)
		t.isaCtl = t.isaHeader.value

	case segNameIEA:
		err := t.checkControlNumber("interchange", t.isaHeader,
			t.end(rawSeg, segNameIEA, elemIEA02, segCount, runeBegin, runeEnd))
		t.isaHeader = envelopeEnd{}
		// The interchange (and thus any group/transaction in it) is over.
		t.stSeen = false
		return err

	case segNameGS:
		// A new functional group starts; any transaction context of the previous group is over.
		t.stSeen = false
		t.stHeader = envelopeEnd{}
		t.gsDate, _ = t.elemValue(rawSeg, elemGS04)
		t.gsTime, _ = t.elemValue(rawSeg, elemGS05)
		t.gsHeader = t.end(rawSeg, segNameGS, elemGS06, segCount, runeBegin, runeEnd)
		t.gsCtl = t.gsHeader.value

	case segNameGE:
		err := t.checkControlNumber("group", t.gsHeader,
			t.end(rawSeg, segNameGE, elemGE02, segCount, runeBegin, runeEnd))
		t.gsHeader = envelopeEnd{}
		// The functional group (and thus any transaction in it) is over.
		t.stSeen = false
		return err

	case segNameST:
		t.stSeen = true
		t.stType, _ = t.elemValue(rawSeg, elemST01)
		t.stHeader = t.end(rawSeg, segNameST, elemST02, segCount, runeBegin, runeEnd)
		t.stCtl = t.stHeader.value

	case segNameSE:
		err := t.checkControlNumber("transaction set", t.stHeader,
			t.end(rawSeg, segNameSE, elemSE02, segCount, runeBegin, runeEnd))
		// The transaction is now closed. Keep t.stType/t.stCtl around (harmless), but targets
		// starting after SE must not claim the closed transaction's identity.
		t.stHeader = envelopeEnd{}
		t.stSeen = false
		return err
	}
	return nil
}

func (t *envelopeTracker) checkControlNumber(level string, header, trailer envelopeEnd) error {
	if !t.validate {
		return nil
	}
	if !header.present && !trailer.present {
		// Neither end carries a control number, e.g. simplified/partial input or an EDI flavor
		// that doesn't use X12 control numbers. Nothing to pair, nothing to report.
		return nil
	}
	if header.present && trailer.present && sameControlNumber(header.value, trailer.value) {
		return nil
	}
	return fmt.Errorf("%s control number mismatch: %s; %s", level, header.String(), trailer.String())
}

// sameControlNumber compares two control number values. Control numbers are numeric in X12;
// some trading partners drop the leading zeros in the trailer (e.g. ISA13 '000001644' vs IEA02
// '1644'), which is semantically the same control number. Only when both values are entirely
// digits do we treat leading zeros as insignificant; otherwise an exact string match is required.
func sameControlNumber(a, b string) bool {
	if a == b {
		return true
	}
	if !isAllDigits(a) || !isAllDigits(b) {
		return false
	}
	trim := func(s string) string { return strings.TrimLeft(s, "0") }
	return trim(a) == trim(b)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// identity returns the identity snapshot in effect right now, and whether it is meaningful (that
// is, whether the reader currently is inside a transaction set).
func (t *envelopeTracker) identity() (envelopeIdentity, bool) {
	if !t.outputIdentity || !t.stSeen {
		return envelopeIdentity{}, false
	}
	return envelopeIdentity{
		partnerID:         t.partnerID(),
		transactionID:     t.transactionID(),
		senderQualifier:   t.senderQualifier,
		senderID:          t.senderID,
		receiverQualifier: t.receiverQualifier,
		receiverID:        t.receiverID,
		isaCtl:            t.isaCtl,
		isaDate:           t.isaDate,
		isaTime:           t.isaTime,
		gsCtl:             t.gsCtl,
		gsDate:            t.gsDate,
		gsTime:            t.gsTime,
		stType:            t.stType,
		stCtl:             t.stCtl,
	}, true
}

// partnerID returns the stable trading partner identity: interchange sender and receiver, each
// as 'qualifier:id'. Empty if no ISA has been seen.
func (t *envelopeTracker) partnerID() string {
	if !t.isaSeen {
		return ""
	}
	return "sender=" + t.senderQualifier + ":" + t.senderID +
		"|receiver=" + t.receiverQualifier + ":" + t.receiverID
}

// transactionID returns the stable transaction identity. It incorporates the interchange control
// number and delivery date/time (ISA09/ISA10), the group control number and date/time
// (GS04/GS05), and the transaction set type/control number (ST01/ST02). Reusing an interchange
// control number with a different delivery time therefore yields a different identity.
func (t *envelopeTracker) transactionID() string {
	return "isa=" + t.isaCtl + "@" + t.isaDate + "T" + t.isaTime +
		"|gs=" + t.gsCtl + "@" + t.gsDate + "T" + t.gsTime +
		"|st=" + t.stType + ":" + t.stCtl
}

func addTextChild(parent *idr.Node, name, value string) {
	n := idr.CreateNode(idr.ElementNode, name)
	idr.AddChild(parent, n)
	idr.AddChild(n, idr.CreateNode(idr.TextNode, value))
}
