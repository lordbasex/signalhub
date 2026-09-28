// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package turncred

import "testing"

func TestCredentials(t *testing.T) {
	// Reference value computed with:
	//   printf '1700000000:peer' | openssl dgst -sha1 -hmac secret -binary | base64
	user, cred := Credentials("secret", "peer", 1700000000)
	if user != "1700000000:peer" {
		t.Fatalf("username = %q", user)
	}
	if cred != "m9GnlZWvaZKjLjGseKws6UpgiA0=" {
		t.Fatalf("credential = %q", cred)
	}
}

func TestCredentialsDependOnSecret(t *testing.T) {
	_, a := Credentials("secret-a", "peer", 1)
	_, b := Credentials("secret-b", "peer", 1)
	if a == b {
		t.Fatal("different secrets produced the same credential")
	}
}
