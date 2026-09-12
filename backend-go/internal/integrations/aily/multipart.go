package aily

import (
	"bytes"
	"net/textproto"
)

// multipartWriter builds a minimal multipart/form-data body (the Aily
// attachment endpoint is the only multipart consumer).
type multipartWriter struct {
	buf      *bytes.Buffer
	boundary string
}

func newMultipartWriter(buf *bytes.Buffer, boundary string) *multipartWriter {
	return &multipartWriter{buf: buf, boundary: boundary}
}

func (w *multipartWriter) writeBoundary() {
	w.buf.WriteString("--" + w.boundary + "\r\n")
}

func (w *multipartWriter) addField(name, value string) error {
	w.writeBoundary()
	hdr := textproto.CanonicalMIMEHeaderKey("Content-Disposition") + `: form-data; name="` + name + `"` + "\r\n\r\n"
	w.buf.WriteString(hdr)
	w.buf.WriteString(value)
	w.buf.WriteString("\r\n")
	return nil
}

func (w *multipartWriter) addFile(name, filename string, data []byte) error {
	w.writeBoundary()
	w.buf.WriteString(`Content-Disposition: form-data; name="` + name + `"; filename="` + filename + `"` + "\r\n")
	w.buf.WriteString("Content-Type: application/octet-stream\r\n\r\n")
	w.buf.Write(data)
	w.buf.WriteString("\r\n")
	return nil
}

func (w *multipartWriter) close() error {
	w.buf.WriteString("--" + w.boundary + "--\r\n")
	return nil
}
