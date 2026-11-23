package edi

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jf-tech/omniparser/idr"
)

const envelopeTestDeclJSON = `
{
	"segment_delimiter": "~",
	"element_delimiter": "*",
	"segment_declarations": [
		{
			"name": "ISA",
			"child_segments": [
				{
					"name": "GS",
					"child_segments": [
						{
							"name": "transactions",
							"type": "segment_group",
							"min": 0,
							"max": -1,
							"is_target": true,
							"child_segments": [
								{ "name": "ST" },
								{ "name": "BGN" },
								{ "name": "SE" }
							]
						}
					]
				},
				{ "name": "GE" }
			]
		},
		{ "name": "IEA" }
	]
}`

// envelopeInput builds a tiny X12 interchange with the given control numbers/date-times. Each
// transaction has one BGN segment so it produces a target record.
func envelopeInput(isaCtl, isaDate, isaTime, gsCtl, gsDate, gsTime string,
	stCtls, seCtls []string, ieaCtl, geCtl string) string {
	return "ISA*00*          *00*          *02*SENDER         *ZZ*RECEIVER       *" +
		isaDate + "*" + isaTime + "*U*00401*" + isaCtl + "*0*P*>~" +
		"GS*BE*SENDER*RECEIVER*" + gsDate + "*" + gsTime + "*" + gsCtl + "*X*004010~" +
		func() string {
			var b strings.Builder
			for i := range stCtls {
				b.WriteString("ST*834*" + stCtls[i] + "~")
				b.WriteString("BGN*00*" + stCtls[i] + "*" + isaDate + "~")
				b.WriteString("SE*3*" + seCtls[i] + "~")
			}
			return b.String()
		}() +
		"GE*1*" + geCtl + "~" +
		"IEA*1*" + ieaCtl + "~"
}

// readAll reads all records. Note record nodes are only valid until the next Read() call (the
// reader recycles them), so they are snapshotted as JSON immediately.
func readAll(t *testing.T, decl *FileDecl, input string) ([]string, []error) {
	r, err := NewReader("test", strings.NewReader(input), decl, "")
	assert.NoError(t, err)
	var records []string
	var errs []error
	for {
		n, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			errs = append(errs, err)
			if IsErrInvalidEDI(err) {
				break
			}
			continue
		}
		records = append(records, idr.JSONify2(n))
	}
	return records, errs
}

func envelopeOf(t *testing.T, recordJSON string) map[string]interface{} {
	var record map[string]interface{}
	assert.NoError(t, json.Unmarshal([]byte(recordJSON), &record))
	env, ok := record[envelopeIdentityElem]
	if !ok {
		return nil
	}
	return env.(map[string]interface{})
}

func TestEnvelope_Track_MatchingControlCodes(t *testing.T) {
	var decl FileDecl
	assert.NoError(t, json.Unmarshal([]byte(envelopeTestDeclJSON), &decl))
	input := envelopeInput("000001644", "191103", "1800", "000001644", "20191103", "1800",
		[]string{"000000001", "000000002"}, []string{"000000001", "000000002"},
		"000001644", "000001644")
	nodes, errs := readAll(t, &decl, input)
	assert.Empty(t, errs)
	assert.Len(t, nodes, 2)
	for _, n := range nodes {
		assert.NotNil(t, envelopeOf(t, n))
	}
}

func TestEnvelope_ControlCodeMismatch_ReportsBothEnds(t *testing.T) {
	for _, test := range []struct {
		name      string
		isaCtl    string
		gsCtl     string
		stCtls    []string
		seCtls    []string
		ieaCtl    string
		geCtl     string
		wantLevel string
		wantHdr   string
		wantFtr   string
	}{
		{
			name:      "interchange ISA13 != IEA02",
			isaCtl:    "000001644",
			gsCtl:     "000001644",
			stCtls:    []string{"000000001"},
			seCtls:    []string{"000000001"},
			ieaCtl:    "000009999",
			geCtl:     "000001644",
			wantLevel: "interchange",
			wantHdr:   "segment 'ISA' at segment no.1",
			wantFtr:   "segment 'IEA' at segment no.7",
		},
		{
			name:      "group GS06 != GE02",
			isaCtl:    "000001644",
			gsCtl:     "000001644",
			stCtls:    []string{"000000001"},
			seCtls:    []string{"000000001"},
			ieaCtl:    "000001644",
			geCtl:     "000002767",
			wantLevel: "group",
			wantHdr:   "segment 'GS' at segment no.2",
			wantFtr:   "segment 'GE' at segment no.6",
		},
		{
			name:      "transaction ST02 != SE02",
			isaCtl:    "000001644",
			gsCtl:     "000001644",
			stCtls:    []string{"000000001"},
			seCtls:    []string{"000000002"},
			ieaCtl:    "000001644",
			geCtl:     "000001644",
			wantLevel: "transaction set",
			wantHdr:   "segment 'ST' at segment no.3",
			wantFtr:   "segment 'SE' at segment no.5",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var decl FileDecl
			assert.NoError(t, json.Unmarshal([]byte(envelopeTestDeclJSON), &decl))
			input := envelopeInput(test.isaCtl, "191103", "1800", test.gsCtl, "20191103", "1800",
				test.stCtls, test.seCtls, test.ieaCtl, test.geCtl)
			_, errs := readAll(t, &decl, input)
			assert.NotEmpty(t, errs)
			var found bool
			for _, err := range errs {
				msg := err.Error()
				if strings.Contains(msg, test.wantLevel+" control number mismatch") {
					found = true
					assert.Contains(t, msg, test.wantHdr)
					assert.Contains(t, msg, test.wantFtr)
					assert.Contains(t, msg, "test") // input name
				}
			}
			assert.True(t, found, "expected %s mismatch, got: %v", test.wantLevel, errs)
		})
	}
}

func TestEnvelope_MismatchErrorIsContinuableAndRecordStillYielded(t *testing.T) {
	var decl FileDecl
	assert.NoError(t, json.Unmarshal([]byte(envelopeTestDeclJSON), &decl))
	// Two interchanges; the first has a mismatched IEA control number.
	first := envelopeInput("000001644", "191103", "1800", "000001644", "20191103", "1800",
		[]string{"000000001"}, []string{"000000001"}, "000009999", "000001644")
	second := envelopeInput("000001645", "191104", "0900", "000001645", "20191104", "0900",
		[]string{"000000002"}, []string{"000000002"}, "000001645", "000001645")
	r, err := NewReader("test", strings.NewReader(first+second), &decl, "")
	assert.NoError(t, err)

	var records []string
	var envelopeErrs []error
	for {
		n, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			if !strings.Contains(err.Error(), "control number mismatch") {
				t.Fatalf("unexpected error: %v", err)
			}
			envelopeErrs = append(envelopeErrs, err)
			if !r.IsContinuableError(err) {
				t.Fatalf("control code mismatch should be continuable, got: %v", err)
			}
			continue
		}
		records = append(records, idr.JSONify2(n))
	}
	// Both transactions (one from the bad interchange, one from the good one) must be yielded.
	assert.Len(t, records, 2)
	assert.Len(t, envelopeErrs, 1)
}

func TestEnvelope_ReusedInterchangeControlNumberDifferentDeliveryTime(t *testing.T) {
	var decl FileDecl
	assert.NoError(t, json.Unmarshal([]byte(envelopeTestDeclJSON), &decl))
	// Same ISA13/GS06/ST02 and same transaction content, but a different delivery time.
	input1 := envelopeInput("000001644", "191103", "1800", "000001644", "20191103", "1800",
		[]string{"000000001"}, []string{"000000001"}, "000001644", "000001644")
	input2 := envelopeInput("000001644", "191103", "1801", "000001644", "20191103", "1801",
		[]string{"000000001"}, []string{"000000001"}, "000001644", "000001644")

	nodes1, errs1 := readAll(t, &decl, input1)
	nodes2, errs2 := readAll(t, &decl, input2)
	assert.Empty(t, errs1)
	assert.Empty(t, errs2)
	assert.Len(t, nodes1, 1)
	assert.Len(t, nodes2, 1)

	env1 := envelopeOf(t, nodes1[0])
	env2 := envelopeOf(t, nodes2[0])
	assert.Equal(t, env1["partner_id"], env2["partner_id"])
	assert.NotEqual(t, env1["transaction_id"], env2["transaction_id"])
	// The raw records (and therefore their checksums) must differ so downstream dedup won't
	// collapse the two genuinely different transactions.
	assert.NotEqual(t, nodes1[0], nodes2[0])

	// Same input twice must produce identical identities (stable).
	stable1, _ := readAll(t, &decl, input1)
	assert.Equal(t, env1, envelopeOf(t, stable1[0]))
}

func TestEnvelope_IdentityIsStableAcrossRecordsOfSameTransaction(t *testing.T) {
	var decl FileDecl
	assert.NoError(t, json.Unmarshal([]byte(envelopeTestDeclJSON), &decl))
	// Two transactions in the same interchange: partner_id is the same, transaction_id differs.
	input := envelopeInput("000001644", "191103", "1800", "000001644", "20191103", "1800",
		[]string{"000000001", "000000002"}, []string{"000000001", "000000002"},
		"000001644", "000001644")
	nodes, errs := readAll(t, &decl, input)
	assert.Empty(t, errs)
	assert.Len(t, nodes, 2)
	env1 := envelopeOf(t, nodes[0])
	env2 := envelopeOf(t, nodes[1])
	assert.Equal(t, env1["partner_id"], env2["partner_id"])
	assert.NotEqual(t, env1["transaction_id"], env2["transaction_id"])
}

func TestEnvelope_OutputIdentityDisabled(t *testing.T) {
	declJSON := strings.Replace(
		envelopeTestDeclJSON, `"segment_delimiter": "~",`,
		`"segment_delimiter": "~", "envelope": { "output_identity": false },`, 1)
	var decl FileDecl
	assert.NoError(t, json.Unmarshal([]byte(declJSON), &decl))
	input := envelopeInput("000001644", "191103", "1800", "000001644", "20191103", "1800",
		[]string{"000000001"}, []string{"000000001"}, "000001644", "000001644")
	nodes, errs := readAll(t, &decl, input)
	assert.Empty(t, errs)
	assert.Len(t, nodes, 1)
	assert.Nil(t, envelopeOf(t, nodes[0]))
}

func TestEnvelope_ValidationDisabled(t *testing.T) {
	declJSON := strings.Replace(
		envelopeTestDeclJSON, `"segment_delimiter": "~",`,
		`"segment_delimiter": "~", "envelope": { "validate_control_codes": false },`, 1)
	var decl FileDecl
	assert.NoError(t, json.Unmarshal([]byte(declJSON), &decl))
	input := envelopeInput("000001644", "191103", "1800", "000001644", "20191103", "1800",
		[]string{"000000001"}, []string{"000000002"}, "000009999", "000002767")
	nodes, errs := readAll(t, &decl, input)
	assert.Empty(t, errs)
	assert.Len(t, nodes, 1)
	// Identity output is still on by default.
	assert.NotNil(t, envelopeOf(t, nodes[0]))
}

func TestEnvelope_NonX12FlavorUnaffected(t *testing.T) {
	// EDIFACT-ish input: envelope segment names UNB/UNZ/UNH/UNT aren't X12 ones; no tracking,
	// no identity, no errors.
	var decl FileDecl
	assert.NoError(t, json.Unmarshal([]byte(`
		{
			"segment_delimiter": "'",
			"element_delimiter": "+",
			"segment_declarations": [
				{
					"name": "UNB",
					"child_segments": [
						{
							"name": "messages",
							"type": "segment_group",
							"min": 0,
							"max": -1,
							"is_target": true,
							"child_segments": [ { "name": "UNH" }, { "name": "BGM" }, { "name": "UNT" } ]
						}
					]
				},
				{ "name": "UNZ" }
			]
		}`), &decl))
	input := "UNB+UNOC:3+SENDER+RECEIVER+202502:1715+1'UNH+1+IFTSTA'BGM+77+123+9'UNT+3+1'UNZ+1+1'"
	nodes, errs := readAll(t, &decl, input)
	assert.Empty(t, errs)
	assert.Len(t, nodes, 1)
	assert.Nil(t, envelopeOf(t, nodes[0]))
}

func TestEnvelopeTracker_Track_Unit(t *testing.T) {
	tracker := newEnvelopeTracker(&EnvelopeDecl{}, nil)
	raw := func(name string, vals ...string) RawSeg {
		seg := RawSeg{valid: true, Name: name}
		for i, v := range vals {
			seg.Elems = append(seg.Elems, RawSegElem{ElemIndex: i + 1, CompIndex: 1, Data: []byte(v)})
		}
		return seg
	}
	// ISA elements 5..10 and 13; elements 1..4 omitted but indexes preserved via vals layout.
	isa := raw("ISA", "00", " ", "00", " ", "02", "SENDER", "ZZ", "RECEIVER", "191103", "1800",
		"U", "00401", "000001644", "0", "P", ">")
	assert.NoError(t, tracker.track(isa, 1, 0, 0))
	assert.Equal(t, "sender=02:SENDER|receiver=ZZ:RECEIVER", tracker.partnerID())
	assert.Equal(t, "isa=000001644@191103T1800|gs=@T|st=:", tracker.transactionID())
	assert.False(t, tracker.stSeen)
	_, ok := tracker.identity()
	assert.False(t, ok)

	assert.NoError(t, tracker.track(raw("GS", "BE", "S", "R", "20191103", "1800", "000001644"), 2, 0, 0))
	assert.NoError(t, tracker.track(raw("ST", "834", "000000001"), 3, 0, 0))
	id, ok := tracker.identity()
	assert.True(t, ok)
	assert.Equal(t, "isa=000001644@191103T1800|gs=000001644@20191103T1800|st=834:000000001", id.transactionID)

	assert.NoError(t, tracker.track(raw("SE", "3", "000000001"), 4, 0, 0))
	assert.NoError(t, tracker.track(raw("GE", "1", "000001644"), 5, 0, 0))
	assert.NoError(t, tracker.track(raw("IEA", "1", "000001644"), 6, 0, 0))
}

func TestEnvelopeTracker_MismatchReportsEnds(t *testing.T) {
	tracker := newEnvelopeTracker(&EnvelopeDecl{}, nil)
	raw := func(name string, vals ...string) RawSeg {
		seg := RawSeg{valid: true, Name: name}
		for i, v := range vals {
			seg.Elems = append(seg.Elems, RawSegElem{ElemIndex: i + 1, CompIndex: 1, Data: []byte(v)})
		}
		return seg
	}
	assert.NoError(t, tracker.track(
		raw("ST", "834", "000000001"), 3, 100, 110))
	err := tracker.track(raw("SE", "3", "000000002"), 7, 200, 210)
	assert.Error(t, err)
	assert.Equal(t,
		`transaction set control number mismatch: `+
			`segment 'ST' at segment no.3 (char[100,110]) has control number '000000001'; `+
			`segment 'SE' at segment no.7 (char[200,210]) has control number '000000002'`,
		err.Error())
}

func TestEnvelopeDecl_Defaults(t *testing.T) {
	var nilDecl *EnvelopeDecl
	assert.True(t, nilDecl.validateControlCodes())
	assert.True(t, nilDecl.outputIdentity())

	var empty EnvelopeDecl
	assert.True(t, empty.validateControlCodes())
	assert.True(t, empty.outputIdentity())

	f, _ := false, false
	empty.ValidateControlCodes = &f
	empty.OutputIdentity = &f
	assert.False(t, empty.validateControlCodes())
	assert.False(t, empty.outputIdentity())
}

func TestSameControlNumber(t *testing.T) {
	assert.True(t, sameControlNumber("000001644", "000001644"))
	assert.True(t, sameControlNumber("000001644", "1644"))
	assert.True(t, sameControlNumber("0", "000"))
	assert.True(t, sameControlNumber("ABC", "ABC"))
	assert.False(t, sameControlNumber("000001644", "000001645"))
	assert.False(t, sameControlNumber("ABC", "abc"))
	// Non-numeric values must not be leading-zero-normalized.
	assert.False(t, sameControlNumber("00A", "A"))
}

func TestEnvelope_EmptyInputNoIdentity(t *testing.T) {
	var decl FileDecl
	assert.NoError(t, json.Unmarshal([]byte(envelopeTestDeclJSON), &decl))
	nodes, _ := readAll(t, &decl, "")
	assert.Empty(t, nodes)
}

func TestEnvelope_IdentityEscaping(t *testing.T) {
	// Ensuring field separators in values don't corrupt the identity string.
	assert.Equal(t, "sender=02:A|receiver=ZZ:C", func() string {
		tracker := newEnvelopeTracker(&EnvelopeDecl{}, nil)
		raw := RawSeg{valid: true, Name: "ISA", Elems: []RawSegElem{
			{5, 1, []byte("02")}, {6, 1, []byte("A")}, {7, 1, []byte("ZZ")}, {8, 1, []byte("C")},
			{9, 1, []byte("191103")}, {10, 1, []byte("1800")}, {13, 1, []byte("1")},
		}}
		_ = tracker.track(raw, 1, 0, 0)
		return tracker.partnerID()
	}())
}
