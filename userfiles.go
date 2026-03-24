package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/andreyvit/jsonfix"
	"github.com/andreyvit/naml"
)

// decodeUserFile parses a user-editable JSON/NAML ("yaml") file.
// For .yaml/.yml it runs naml.Convert and errors are fatal.
// Then it applies jsonfix (comments/trailing commas) and decodes with stdlib json.
func decodeUserFile(fn string, out any) error {
	raw, err := os.ReadFile(fn)
	if err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(fn))
	if ext == ".yaml" || ext == ".yml" {
		converted, err := naml.Convert(raw)
		if err != nil {
			return fmt.Errorf("naml convert %s: %w", filepath.Base(fn), err)
		}
		raw = converted
	}
	raw = jsonfix.Bytes(raw)

	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing content")
		}
		return err
	}
	return nil
}
