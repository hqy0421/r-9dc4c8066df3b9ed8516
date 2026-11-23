package omniparser

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jf-tech/omniparser/errs"
	"github.com/jf-tech/omniparser/transformctx"
)

const ediEnvelopeTestSchema = `
{
	"parser_settings": { "version": "omni.2.1", "file_format_type": "edi" },
	"file_declaration": {
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
								"min": 0, "max": -1, "is_target": true,
								"child_segments": [ { "name": "ST" }, { "name": "BGN" }, { "name": "SE" } ]
							}
						]
					},
					{ "name": "GE" }
				]
			},
			{ "name": "IEA" }
		]
	},
	"transform_declarations": {
		"FINAL_OUTPUT": { "object": {
			"bgn": { "xpath": "BGN/bgn" },
			"transaction_id": { "xpath": "__envelope/transaction_id" }
		}}
	}
}`

func ediEnvelopeInput(isaCtl, isaTime, stCtl, seCtl, ieaCtl string) string {
	return "ISA*00* *00* *02*S*ZZ*R*191103*" + isaTime + "*U*00401*" + isaCtl + "*0*P*>~" +
		"GS*BE*S*R*20191103*" + isaTime + "*" + isaCtl + "*X*004010~" +
		"ST*834*" + stCtl + "~BGN*00*abc*191103~SE*3*" + seCtl + "~" +
		"GE*1*" + isaCtl + "~IEA*1*" + ieaCtl + "~"
}

// TestEDIEnvelope_ControlCodeMismatchIsContinuableAndReportsBothEnds verifies that, at the
// public API level, a transaction whose header/trailer control codes don't pair produces a
// continuable ErrTransformFailed error that names both ends, while still yielding the records
// of the (valid) following transaction.
func TestEDIEnvelope_ControlCodeMismatchIsContinuableAndReportsBothEnds(t *testing.T) {
	schema, err := NewSchema("test", strings.NewReader(ediEnvelopeTestSchema))
	assert.NoError(t, err)
	bad := ediEnvelopeInput("000001644", "1800", "000000001", "000000002", "000001644")
	good := ediEnvelopeInput("000001645", "0900", "000000002", "000000002", "000001645")
	transform, err := schema.NewTransform(
		"input.edi", strings.NewReader(bad+good), &transformctx.Ctx{})
	assert.NoError(t, err)

	var okCount, mismatchCount int
	for {
		_, err := transform.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			assert.True(t, errs.IsErrTransformFailed(err), "err should be continuable: %v", err)
			msg := err.Error()
			assert.Contains(t, msg, "transaction set control number mismatch")
			assert.Contains(t, msg, "segment 'ST'")
			assert.Contains(t, msg, "segment 'SE'")
			assert.Contains(t, msg, "000000001")
			assert.Contains(t, msg, "000000002")
			mismatchCount++
			continue
		}
		okCount++
	}
	assert.Equal(t, 1, mismatchCount)
	// Records of BOTH transactions are still produced: the one from the mismatched envelope
	// (yielded before the error is reported) and the one from the valid following transaction.
	assert.Equal(t, 2, okCount)
}

// TestEDIEnvelope_ReusedInterchangeControlNumberDifferentDeliveryTime verifies that two
// transactions which reuse the same interchange control number but have different delivery times
// get different raw record checksums / transaction ids, so downstream de-duplication can't
// swallow one of them.
func TestEDIEnvelope_ReusedInterchangeControlNumberDifferentDeliveryTime(t *testing.T) {
	schema, err := NewSchema("test", strings.NewReader(ediEnvelopeTestSchema))
	assert.NoError(t, err)

	read := func(input string) (checksum string, transformed string) {
		transform, err := schema.NewTransform("input.edi", strings.NewReader(input), &transformctx.Ctx{})
		assert.NoError(t, err)
		b, err := transform.Read()
		assert.NoError(t, err)
		raw, err := transform.RawRecord()
		assert.NoError(t, err)
		return raw.Checksum(), string(b)
	}

	checksum1, transformed1 := read(ediEnvelopeInput("000001644", "1800", "000000001", "000000001", "000001644"))
	checksum2, transformed2 := read(ediEnvelopeInput("000001644", "1801", "000000001", "000000001", "000001644"))
	assert.NotEqual(t, checksum1, checksum2,
		"records under a reused interchange control number but different delivery time must not have equal checksums")
	assert.NotEqual(t, transformed1, transformed2,
		"transaction_id in transformed output must differ when delivery time differs")
}
