package mailer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

// Address is an email address with an optional display name.
//
// In JSON it accepts either an object ({"email": "a@b.com", "name": "A"})
// or a plain string ("a@b.com" or "A <a@b.com>").
type Address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// UnmarshalJSON accepts both the object and the string form.
func (a *Address) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		parsed, err := mail.ParseAddress(s)
		if err != nil {
			return fmt.Errorf("invalid email address %q", s)
		}
		*a = Address{Email: parsed.Address, Name: parsed.Name}
		return nil
	}
	type plain Address
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*a = Address(p)
	return nil
}

// Validate checks that the address is a single, well-formed mailbox.
func (a Address) Validate() error {
	if a.Email == "" {
		return errors.New("email is required")
	}
	if strings.ContainsAny(a.Email, "\r\n") || strings.ContainsAny(a.Name, "\r\n") {
		return errors.New("address must not contain line breaks")
	}
	parsed, err := mail.ParseAddress(a.Email)
	if err != nil || parsed.Address != a.Email {
		return fmt.Errorf("invalid email address %q", a.Email)
	}
	return nil
}

// String formats the address for use in a message header, encoding
// non-ASCII display names as needed.
func (a Address) String() string {
	return (&mail.Address{Name: a.Name, Address: a.Email}).String()
}

// Domain returns the part after the last "@".
func (a Address) Domain() string {
	if i := strings.LastIndexByte(a.Email, '@'); i >= 0 {
		return a.Email[i+1:]
	}
	return "localhost"
}
