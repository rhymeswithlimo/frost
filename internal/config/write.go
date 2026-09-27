package config

import (
	"os"
	"path/filepath"
)

// WritePrivate replaces a local secret through a unique, private temporary
// file. Failed writes leave the previous file intact.
func WritePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".frost-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
