// Code generated - DO NOT EDIT.

package validation

const (
    JSONSchemaEDIFileDeclaration =
`
{
    "$schema": "http://json-schema.org/draft-07/schema#",
    "$id": "github.com/jf-tech/omniparser:edi_file_declaration",
    "title": "omniparser schema: edi/file_declaration",
    "type": "object",
    "properties": {
        "file_declaration": {
            "type": "object",
            "properties": {
                "segment_delimiter": { "type": "string", "minLength": 1 },
                "element_delimiter": { "type": "string", "minLength": 1 },
                "component_delimiter": { "type": "string", "minLength": 1 },
                "repetition_delimiter": { "type": "string", "minLength": 1 },
                "release_character": { "type": "string", "minLength": 1 },
                "ignore_crlf": { "type": "boolean" },
                "envelope": {
                    "type": "object",
                    "properties": {
                        "validate_control_codes": { "type": "boolean", "$comment": "default true; validate ISA13==IEA02, GS06==GE02, ST02==SE02" },
                        "output_identity": { "type": "boolean", "$comment": "default true; emit stable partner/transaction identity in each target record" }
                    },
                    "additionalProperties": false
                },
                "segment_declarations": {
                    "type": "array",
                    "items": {
                      "$ref": "#/definitions/segment_declaration_type"
                    }
                }
            },
            "required": [ "segment_delimiter", "element_delimiter", "segment_declarations" ],
            "additionalProperties": false
        }
    },
    "required": [ "file_declaration" ],
    "definitions": {
        "segment_declaration_type": {
            "type": "object",
            "properties": {
                "name": { "type": "string", "minLength": 1 },
                "type": { "type": "string", "enum": [ "segment", "segment_group" ] },
                "is_target": { "type": "boolean" },
                "min": { "type": "integer", "minimum": 0 },
                "max": { "type": "integer", "minimum": -1 },
                "elements": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "name": { "type": "string", "minLength": 1 },
                            "index": { "type": "integer", "minimum": 1 },
                            "component_index": { "type": "integer", "minimum": 1 },
                            "empty_if_missing": { "type": "boolean","$comment": "deprecated, use 'default'" },
                            "default": { "type": "string" },
                            "_comment": { "$ref": "#/definitions/value_comment" }
                        },
                        "required": [ "name", "index" ],
                        "additionalProperties": false
                    }
                },
                "child_segments": {
                    "type": "array",
                    "items": {
                      "$ref": "#/definitions/segment_declaration_type"
                    }
                },
                "_comment": { "$ref": "#/definitions/value_comment" }
            },
            "required": [ "name" ],
            "additionalProperties": false
        },
        "value_comment": { "type": "string" }
    }
}

`
)
