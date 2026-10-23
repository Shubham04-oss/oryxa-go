package spicedb

import "testing"

func TestDial(t *testing.T) {
	if _, err := Dial(Config{}); err == nil {
		t.Fatal("Dial with no endpoint succeeded")
	}
	if _, err := Dial(Config{Endpoint: "127.0.0.1:50052"}); err == nil {
		t.Fatal("Dial with no key succeeded")
	}
	client, err := Dial(Config{Endpoint: "127.0.0.1:50052", PresharedKey: "k", Insecure: true})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	client.Close()
}
