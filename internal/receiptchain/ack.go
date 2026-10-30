package receiptchain

import (
	"bytes"
	"encoding/json"
	"io"
)

type TransportRequest struct {
	Protocol     string `json:"protocol"`
	CycleID      string `json:"cycle_id"`
	ReaderSHA256 string `json:"reader_sha256"`
}
type InputAcknowledgement struct {
	Protocol     string `json:"protocol"`
	CycleID      string `json:"cycle_id"`
	ReaderSHA256 string `json:"reader_sha256"`
	InputSHA256  string `json:"input_sha256"`
}

func DecodeInputAcknowledgement(raw []byte) (InputAcknowledgement, error) {
	var a InputAcknowledgement
	if len(raw) > 2048 || !validJSONUnicode(raw) {
		return a, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if e := scanValue(d, 0); e != nil {
		return a, e
	}
	if _, e := d.Token(); e != io.EOF {
		return a, ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&a); e != nil {
		return a, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 4 || a.Protocol != Protocol || !label(a.CycleID, 128) || !validSHA(a.ReaderSHA256) || !validSHA(a.InputSHA256) {
		return a, ErrInvalid
	}
	return a, nil
}

func DecodeResponseInputAcknowledgement(raw []byte) (InputAcknowledgement, error) {
	if len(raw) > 8<<20 || !validJSONUnicode(raw) {
		return InputAcknowledgement{}, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if e := scanValue(d, 0); e != nil {
		return InputAcknowledgement{}, e
	}
	if _, e := d.Token(); e != io.EOF {
		return InputAcknowledgement{}, ErrInvalid
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return InputAcknowledgement{}, ErrInvalid
	}
	return DecodeInputAcknowledgement(object["receipt_chain_ack"])
}
