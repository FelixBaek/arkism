package receiptchain

import (
	"bytes"
	"encoding/json"
	"io"
)

const StorageProtocol = "sys-native-context-receipt-chain-storage-v1"

type StorageEnvelope struct {
	Protocol     string `json:"protocol"`
	Record       Record `json:"receipt_chain"`
	BundleSHA256 string `json:"bundle_sha256"`
	Page         int64  `json:"page"`
	PageCount    int64  `json:"page_count"`
	RecordCount  int64  `json:"record_count"`
}

func DecodeStorageEnvelope(raw []byte) (StorageEnvelope, error) {
	var e StorageEnvelope
	if len(raw) > 64<<10 || !validJSONUnicode(raw) {
		return e, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanValue(decoder, 0); err != nil {
		return e, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return e, ErrInvalid
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&e); err != nil {
		return e, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 6 || bytes.Equal(fields["page"], []byte("null")) {
		return e, ErrInvalid
	}
	if e.Protocol != StorageProtocol || !validSHA(e.BundleSHA256) || e.RecordCount < 1 || e.RecordCount > 256 || e.PageCount != (e.RecordCount+63)/64 || e.Page < 0 || e.Page >= e.PageCount || e.Record.Sequence <= e.Page*64 || e.Record.Sequence > min((e.Page+1)*64, e.RecordCount) {
		return e, ErrInvalid
	}
	if _, err := Decode(fields["receipt_chain"]); err != nil {
		return e, err
	}
	return e, nil
}
