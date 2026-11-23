package edi

import (
	"errors"
	"fmt"
	"io"

	"github.com/antchfx/xpath"
	"github.com/jf-tech/go-corelib/caches"
	"github.com/jf-tech/go-corelib/ios"
	"github.com/jf-tech/go-corelib/strs"

	"github.com/jf-tech/omniparser/idr"
)

type stackEntry struct {
	segDecl  *SegDecl  // the current stack entry's segment decl
	segNode  *idr.Node // the current stack entry segment's IDR node
	curChild int       // which child segment is the current segment is processing.
	occurred int       // how many times the current segment is fully processed.
}

const (
	defaultStackDepth = 10
)

func newStack() []stackEntry {
	return make([]stackEntry, 0, defaultStackDepth)
}

type ediReader struct {
	inputName         string
	releaseChar       strPtrByte
	r                 *NonValidatingReader
	stack             []stackEntry
	target            *idr.Node
	targetXPath       *xpath.Expr
	unprocessedRawSeg RawSeg
	envelope          envelopeTracker
	// targetIdentity is the envelope identity captured when the current target instance
	// started; it is attached to the target node when the target is emitted.
	targetIdentity *envelopeIdentity
	// envelopeErrs queues envelope control number mismatches. They are reported on subsequent
	// Read() calls (after the current target, if any, has been yielded), so a mismatched
	// trailer doesn't make the reader drop an otherwise usable record.
	envelopeErrs []error
}

func inRange(i, lowerBoundInclusive, upperBoundInclusive int) bool {
	return i >= lowerBoundInclusive && i <= upperBoundInclusive
}

// stackTop returns the pointer to the 'frame'-th stack entry from the top.
// 'frame' is optional, if not specified, default 0 (aka the very top of
// the stack) is assumed. Note caller NEVER owns the memory of the returned
// entry, thus caller can use the pointer and its data values inside locally
// but should never cache/save it somewhere for later usage.
func (r *ediReader) stackTop(frame ...int) *stackEntry {
	nth := 0
	if len(frame) == 1 {
		nth = frame[0]
	}
	if !inRange(nth, 0, len(r.stack)-1) {
		panic(fmt.Sprintf("frame requested: %d, but stack length: %d", nth, len(r.stack)))
	}
	return &r.stack[len(r.stack)-nth-1]
}

// shrinkStack removes the top frame of the stack and returns the pointer to the NEW TOP
// FRAME to caller. Note caller NEVER owns the memory of the returned entry, thus caller can
// use the pointer and its data values inside locally but should never cache/save it somewhere
// for later usage.
func (r *ediReader) shrinkStack() *stackEntry {
	if len(r.stack) < 1 {
		panic("stack length is empty")
	}
	r.stack = r.stack[:len(r.stack)-1]
	if len(r.stack) < 1 {
		return nil
	}
	return &r.stack[len(r.stack)-1]
}

// growStack adds a new stack entry to the top of the stack.
func (r *ediReader) growStack(e stackEntry) {
	r.stack = append(r.stack, e)
}

func (r *ediReader) resetRawSeg() {
	resetRawSeg(&r.unprocessedRawSeg)
}

func (r *ediReader) getUnprocessedRawSeg() (RawSeg, error) {
	if r.unprocessedRawSeg.valid {
		return r.unprocessedRawSeg, nil
	}
	rawSeg, err := r.r.Read()
	switch {
	case err == io.EOF:
		return RawSeg{}, io.EOF
	case err != nil:
		return RawSeg{}, ErrInvalidEDI(r.fmtErrStr(err.Error()))
	}
	// Track the envelope for every raw segment as it is read, regardless of whether the schema
	// declares it: control code pairing is a property of the input, not of the schema. A
	// mismatch is remembered and reported on a later Read() call, so a record contained in the
	// mismatched envelope is still yielded before the error.
	err = r.envelope.track(rawSeg, r.r.SegCount(), r.r.RuneBegin(), r.r.RuneEnd())
	if err != nil {
		// Deliberately NOT an ErrInvalidEDI: an envelope control number mismatch is a
		// continuable error, so a genuinely different transaction following the bad envelope
		// still gets ingested instead of being swallowed by an early abort.
		// Both ends of the pairing (with their own segment numbers/positions and values) are
		// already spelled out in err; only prepend the input name.
		r.envelopeErrs = append(r.envelopeErrs,
			fmt.Errorf("input '%s': %s", r.inputName, err.Error()))
	}
	r.unprocessedRawSeg = rawSeg
	r.unprocessedRawSeg.valid = true
	return r.unprocessedRawSeg, nil
}

func (r *ediReader) rawSegToNode(segDecl *SegDecl) (*idr.Node, error) {
	if !r.unprocessedRawSeg.valid {
		panic("unprocessedRawSeg is not valid")
	}
	n := idr.CreateNode(idr.ElementNode, segDecl.Name)
	for _, elemDecl := range segDecl.Elems {
		found := false
		for _, rawElem := range r.unprocessedRawSeg.Elems {
			if rawElem.ElemIndex == elemDecl.Index && rawElem.CompIndex == elemDecl.compIndex() {
				elemN := idr.CreateNode(idr.ElementNode, elemDecl.Name)
				idr.AddChild(n, elemN)
				data := string(strs.ByteUnescape(rawElem.Data, r.releaseChar.b, true))
				elemV := idr.CreateNode(idr.TextNode, data)
				idr.AddChild(elemN, elemV)
				found = true
			}
		}
		if found {
			continue
		}
		if elemDecl.EmptyIfMissing || elemDecl.Default != nil {
			elemN := idr.CreateNode(idr.ElementNode, elemDecl.Name)
			idr.AddChild(n, elemN)
			data := ""
			if elemDecl.Default != nil {
				data = *elemDecl.Default
			}
			elemV := idr.CreateNode(idr.TextNode, data)
			idr.AddChild(elemN, elemV)
			continue
		}
		return nil, ErrInvalidEDI(
			r.fmtErrStr("unable to find element '%s' on segment '%s'", elemDecl.Name, segDecl.fqdn))
	}
	return n, nil
}

// segDone wraps up the processing of an instance of current segment (which includes the processing of
// the instances of its child segments). segDone marks streaming target if necessary. If the number of
// instance occurrences is over the current segment's max limit, segDone calls segNext to move to the
// next segment in sequence; If the number of instances is still within max limit, segDone does no more
// action so the current segment will remain on top of the stack and potentially process more instances
// of this segment. Note: segDone is potentially recursive: segDone -> segNext -> segDone -> ...
func (r *ediReader) segDone() {
	cur := r.stackTop()
	cur.curChild = 0
	cur.occurred++
	if cur.segDecl.IsTarget {
		if r.target != nil {
			panic("r.target != nil")
		}
		if cur.segNode == nil {
			panic("cur.segNode == nil")
		}
		if r.targetXPath == nil || idr.MatchAny(cur.segNode, r.targetXPath) {
			// Attach the envelope identity captured when this target instance started, so
			// every emitted record carries a stable partner/transaction identity (which in
			// turn participates in RawRecord.Checksum()). A target that starts before the
			// transaction set (e.g. it wraps a whole group of transactions) has no single
			// transaction identity, so nothing is attached.
			if r.targetIdentity != nil {
				r.targetIdentity.addTo(cur.segNode)
			}
			r.targetIdentity = nil
			r.target = cur.segNode
		} else {
			r.targetIdentity = nil
			idr.RemoveAndReleaseTree(cur.segNode)
			cur.segNode = nil
		}
	}
	if cur.occurred < cur.segDecl.maxOccurs() {
		return
	}
	// we're here because `cur.occurred >= cur.segDecl.maxOccurs()`
	// and the only path segNext() can fail is to have
	// `cur.occurred < cur.segDecl.minOccurs()`, which means
	// the calling to segNext() from segDone() will never fail,
	// if our validation makes sure min<=max.
	_ = r.segNext()
}

// segNext is called when the top-of-stack (aka current) segment is done its full processing and needs to move
// to the next segment. If the current segment has a subsequent sibling, that sibling will be the next segment;
// If not, it indicates the current segment's parent segment is fully done its processing, thus parent's segDone
// is called. Note: segNext is potentially recursive: segNext -> segDone -> segNext -> ...
func (r *ediReader) segNext() error {
	cur := r.stackTop()
	if cur.occurred < cur.segDecl.minOccurs() {
		// the current values of [begin, end] cover the current instance of the current seg. But the error
		// we're about to create is about the missing of next instance of the current seg. So just use
		// 'end' as 'begin' to make the error msg less confusing.
		return ErrInvalidEDI(r.fmtErrStr2(
			r.r.SegCount(), r.r.RuneEnd(), r.r.RuneEnd(),
			"segment '%s' needs min occur %d, but only got %d",
			strs.FirstNonBlank(cur.segDecl.fqdn, cur.segDecl.Name), cur.segDecl.minOccurs(), cur.occurred))
	}
	if len(r.stack) <= 1 {
		return nil
	}
	cur = r.shrinkStack()
	if cur.curChild < len(cur.segDecl.Children)-1 {
		cur.curChild++
		r.growStack(stackEntry{segDecl: cur.segDecl.Children[cur.curChild]})
		return nil
	}
	r.segDone()
	return nil
}

// Read processes EDI input and returns an instance of the streaming target (aka the segment marked with is_target=true)
// The basic idea is a forever for-loop, inside which it reads out an unprocessed segment data, tries to see
// if the segment data matches what's the current segment decl we're processing: if matches, great, creates a new
// instance of the current segment decl with the data; if not, we call segNext to move the next segment decl inline, and
// continue the for-loop so next iteration, the same unprocessed data will be matched against the new segment decl.
func (r *ediReader) Read() (*idr.Node, error) {
	if r.target != nil {
		// This is just in case Release() isn't called by ingester.
		idr.RemoveAndReleaseTree(r.target)
		r.target = nil
	}
	for {
		if r.target != nil {
			return r.target, nil
		}
		if len(r.envelopeErrs) > 0 {
			// Report an envelope control code mismatch only after any target contained in the
			// mismatched envelope has been yielded. The error is a continuable one (it is not
			// ErrInvalidEDI), so ingestion of subsequent envelopes/transactions can proceed.
			err := r.envelopeErrs[0]
			r.envelopeErrs = r.envelopeErrs[1:]
			return nil, err
		}
		rawSeg, err := r.getUnprocessedRawSeg()
		if err == io.EOF {
			// When the input is done, we still need to verified all the
			// remaining segs' min occurs are satisfied. We can do so by
			// simply keeping on moving to the next seg: we call segNext()
			// once at a time - in case after the segNext() call, the reader
			// yields another target node. We can safely do this (1 segNext()
			// call at a time after we counter EOF) is because getUnprocessedRawSeg()
			// will repeatedly return EOF.
			if len(r.stack) <= 1 {
				return nil, io.EOF
			}
			err = r.segNext()
			if err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		cur := r.stackTop()
		if !cur.segDecl.matchSegName(rawSeg.Name) {
			if len(r.stack) <= 1 {
				return nil, ErrInvalidEDI(r.fmtErrStr2(
					r.r.SegCount(), r.r.RuneEnd(), r.r.RuneEnd(),
					"segment '%s' is either not declared in schema or appears in an invalid order",
					rawSeg.Name))
			}
			err = r.segNext()
			if err != nil {
				return nil, err
			}
			continue
		}
		if !cur.segDecl.isGroup() {
			cur.segNode, err = r.rawSegToNode(cur.segDecl)
			if err != nil {
				return nil, err
			}
			r.resetRawSeg()
		} else {
			cur.segNode = idr.CreateNode(idr.ElementNode, cur.segDecl.Name)
		}
		if cur.segDecl.IsTarget {
			// Capture the envelope identity at the point this target instance starts.
			r.targetIdentity = nil
			if identity, ok := r.envelope.identity(); ok {
				r.targetIdentity = &identity
			}
		}
		if len(r.stack) > 1 {
			idr.AddChild(r.stackTop(1).segNode, cur.segNode)
		}
		if len(cur.segDecl.Children) > 0 {
			r.growStack(stackEntry{segDecl: cur.segDecl.Children[0]})
			continue
		}
		r.segDone()
	}
}

func (r *ediReader) Release(n *idr.Node) {
	if r.target == n {
		r.target = nil
	}
	idr.RemoveAndReleaseTree(n)
}

func (r *ediReader) IsContinuableError(err error) bool {
	return !IsErrInvalidEDI(err) && err != io.EOF
}

func (r *ediReader) FmtErr(format string, args ...interface{}) error {
	return errors.New(r.fmtErrStr(format, args...))
}

func (r *ediReader) fmtErrStr(format string, args ...interface{}) string {
	return r.fmtErrStr2(r.r.SegCount(), r.r.RuneBegin(), r.r.RuneEnd(), format, args...)
}

func (r *ediReader) fmtErrStr2(segCount, runeBegin, runeEnd int, format string, args ...interface{}) string {
	return fmt.Sprintf("input '%s' at segment no.%d (char[%d,%d]): %s",
		r.inputName, segCount, runeBegin, runeEnd, fmt.Sprintf(format, args...))
}

const (
	scannerFlags = ios.ScannerByDelimFlagEofNotAsDelim | ios.ScannerByDelimFlagIncludeDelimInReturn
)

var (
	// ReaderBufSize is the default buf size for EDI reader. Making it too small might increase
	// mem-alloc and gc; making it too big increases the initial memory consumption footprint
	// (unnecessarily) for each reader creation which eventually leads to gc as well. Make it
	// exported so caller can experiment and set their optimal value.
	ReaderBufSize = 128
)

// NewReader creates an FormatReader for EDI file format.
func NewReader(inputName string, r io.Reader, decl *FileDecl, targetXPath string) (*ediReader, error) {
	targetXPathExpr, err := func() (*xpath.Expr, error) {
		if targetXPath == "" || targetXPath == "." {
			return nil, nil
		}
		return caches.GetXPathExpr(targetXPath)
	}()
	if err != nil {
		return nil, fmt.Errorf("invalid target xpath '%s', err: %s", targetXPath, err.Error())
	}
	releaseChar := newStrPtrByte(decl.ReleaseChar)
	reader := &ediReader{
		inputName:         inputName,
		r:                 NewNonValidatingReader(r, decl),
		releaseChar:       releaseChar,
		stack:             newStack(),
		targetXPath:       targetXPathExpr,
		unprocessedRawSeg: newRawSeg(),
		envelope:          newEnvelopeTracker(decl.Envelope, releaseChar.b),
	}
	reader.growStack(stackEntry{
		segDecl: &SegDecl{
			Name:     rootSegName,
			Type:     strs.StrPtr(segTypeGroup),
			Children: decl.SegDecls,
			fqdn:     rootSegName,
		},
		segNode: idr.CreateNode(idr.DocumentNode, rootSegName),
	})
	if len(decl.SegDecls) > 0 {
		reader.growStack(stackEntry{
			segDecl: decl.SegDecls[0],
		})
	}
	return reader, nil
}
