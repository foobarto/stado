package runtime

import "testing"

// Security regression (EP-0015 D2): a memory:write plugin must not be able to
// create an APPROVED (queryable, prompt-injectable) memory. The earlier guard
// only blocked action=="approve"; upsert (defaults empty confidence to
// approved), supersede (forces approved), and any confidence:"approved" payload
// also reach approved. pluginMemoryUpdateDenied must catch them all while still
// allowing the legitimate plugin actions (propose-via-candidate, reject,
// delete, edit-to-candidate).
func TestPluginMemoryUpdateDenied(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		deny    bool
	}{
		// approved-producing paths -> DENY
		{"approve", `{"action":"approve","id":"m1"}`, true},
		{"approve case/space variant", `{"action":" APPROVE ","id":"m1"}`, true},
		{"supersede", `{"action":"supersede","id":"m1","item":{"confidence":"approved"}}`, true},
		{"upsert empty confidence (defaults approved)", `{"action":"upsert","item":{}}`, true},
		{"upsert explicit approved", `{"action":"upsert","item":{"confidence":"approved"}}`, true},
		{"upsert Approved variant", `{"action":"upsert","item":{"confidence":"Approved"}}`, true},
		{"edit to approved", `{"action":"edit","id":"m1","item":{"confidence":"approved"}}`, true},
		{"explicit approved on any action", `{"action":"reject","item":{"confidence":"approved"}}`, true},

		// legitimate plugin actions -> ALLOW
		{"upsert candidate", `{"action":"upsert","item":{"confidence":"candidate"}}`, false},
		{"edit to candidate", `{"action":"edit","id":"m1","item":{"confidence":"candidate"}}`, false},
		{"reject", `{"action":"reject","id":"m1"}`, false},
		{"delete", `{"action":"delete","id":"m1"}`, false},
		{"malformed -> store rejects", `not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pluginMemoryUpdateDenied([]byte(tc.payload)); got != tc.deny {
				t.Errorf("pluginMemoryUpdateDenied(%s) = %v; want %v", tc.payload, got, tc.deny)
			}
		})
	}
}
