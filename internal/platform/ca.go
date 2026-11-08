package platform

import "encoding/base64"

// decodeCA turns a webhook's caBundle into PEM bytes. Unstructured objects
// carry it base64-encoded (it is a []byte field in the typed API).
func decodeCA(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}
