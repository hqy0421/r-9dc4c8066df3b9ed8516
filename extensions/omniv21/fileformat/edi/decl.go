package edi

// EnvelopeDecl describes X12 envelope (interchange/group/transaction) handling settings for
// omniparser reader. Both settings default to true; set them to false to restore the historical
// (lenient, no identity) behavior.
type EnvelopeDecl struct {
	// ValidateControlCodes, when true (default), validates that envelope header/trailer control
	// numbers pair up: ISA13==IEA02 (interchange), GS06==GE02 (group) and ST02==SE02
	// (transaction set). A mismatch is reported (with both ends) as a continuable error.
	ValidateControlCodes *bool `json:"validate_control_codes,omitempty"`
	// OutputIdentity, when true (default), injects a synthetic '__envelope' element into every
	// target inside a transaction set, carrying a stable partner identity (ISA05/06/07/08) and
	// a stable transaction identity (interchange/group/transaction control numbers plus
	// delivery date/time). Being part of the raw record, it also affects RawRecord.Checksum().
	OutputIdentity *bool `json:"output_identity,omitempty"`
}

func (e *EnvelopeDecl) validateControlCodes() bool {
	return e == nil || e.ValidateControlCodes == nil || *e.ValidateControlCodes
}

func (e *EnvelopeDecl) outputIdentity() bool {
	return e == nil || e.OutputIdentity == nil || *e.OutputIdentity
}

// FileDecl describes EDI specific schema settings for omniparser reader.
type FileDecl struct {
	SegDelim    string        `json:"segment_delimiter,omitempty"`
	ElemDelim   string        `json:"element_delimiter,omitempty"`
	CompDelim   *string       `json:"component_delimiter,omitempty"`
	RepDelim    *string       `json:"repetition_delimiter,omitempty"`
	ReleaseChar *string       `json:"release_character,omitempty"`
	IgnoreCRLF  bool          `json:"ignore_crlf,omitempty"`
	Envelope    *EnvelopeDecl `json:"envelope,omitempty"`
	SegDecls    []*SegDecl    `json:"segment_declarations,omitempty"`
}
