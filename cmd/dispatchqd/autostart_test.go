package main

import (
	"errors"
	"testing"
)

var (
	errMissing = errors.New("not found")
	errDenied  = errors.New("access is denied")
)

type fakeKey struct {
	deleteErr error
	deleted   []string
	closed    bool
}

func (k *fakeKey) DeleteValue(name string) error {
	k.deleted = append(k.deleted, name)
	return k.deleteErr
}

func (k *fakeKey) Close() error {
	k.closed = true
	return nil
}

func TestRemoveAutostart(t *testing.T) {
	isMissing := func(err error) bool { return errors.Is(err, errMissing) }
	cases := []struct {
		name      string
		openErr   error
		deleteErr error
		want      error
		deletes   bool
	}{
		{name: "removes the value", deletes: true},
		{name: "missing Run key is already removed", openErr: errMissing},
		{name: "missing value is already removed", deleteErr: errMissing, deletes: true},
		// The integration gap: a Run key that exists but cannot be opened
		// was reported as removed while the autostart entry stayed.
		{name: "unopenable Run key is an error", openErr: errDenied, want: errDenied},
		{name: "failed delete is an error", deleteErr: errDenied, want: errDenied, deletes: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := &fakeKey{deleteErr: tc.deleteErr}
			open := func() (valueDeleter, error) {
				if tc.openErr != nil {
					return nil, tc.openErr
				}
				return k, nil
			}
			err := removeAutostart(open, isMissing, "DispatchQ")
			if tc.want == nil && err != nil {
				t.Fatalf("removeAutostart() = %v, want nil", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("removeAutostart() = %v, want %v", err, tc.want)
			}
			if got := len(k.deleted) == 1 && k.deleted[0] == "DispatchQ"; got != tc.deletes {
				t.Fatalf("deleted %v, want delete=%v", k.deleted, tc.deletes)
			}
			if tc.deletes && !k.closed {
				t.Fatal("the opened key was not closed")
			}
		})
	}
}
