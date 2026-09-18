package persistenthost

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestACPRelayReusedWireIDKeepsNewRequestAtItsCausalPosition(t *testing.T) {
	relay := newTestACPRelay(t)
	relayClientFrame(t, relay, []byte(`{"id":1,"method":"session/prompt","params":{}}`), 1)
	request := []byte(`{"id":7,"method":"session/request_permission","params":{}}`)
	first, _ := relayProviderFrame(t, relay, request, 1, true)
	relayClientFrame(t, relay, []byte(`{"id":7,"result":{}}`), 1)
	relayProviderFrame(t, relay, []byte(`{"method":"session/update","params":{"marker":"between"}}`), 1, true)
	second, _ := relayProviderFrame(t, relay, request, 1, true)
	if frameMetaString(t, first, ACPRequestIDMetaKey) == frameMetaString(t, second, ACPRequestIDMetaKey) {
		t.Fatal("new interaction reused old durable identity")
	}
	replay := relayReplayFrames(t, relay)
	if len(replay) != 2 || !bytes.Contains(replay[0], []byte("between")) || !bytes.Equal(replay[1], second) {
		t.Fatalf("reused request replayed before its cause or twice: %q", replay)
	}
}

func TestACPRelayRejectsPromptUntilPreviousCompletionIsAcknowledged(t *testing.T) {
	relay := newTestACPRelay(t)
	prompt := []byte(`{"id":1,"method":"session/prompt","params":{}}`)
	first := relayClientFrame(t, relay, prompt, 1)
	relayProviderFrame(t, relay, []byte(`{"method":"session/update","params":{"marker":"retained"}}`), 1, true)
	rejected := func() {
		t.Helper()
		before := bytes.Join(relayReplayFrames(t, relay), nil)
		result, err := relay.clientFrame(context.Background(), prompt, 2)
		if err != nil || len(result.provider) != 0 || !bytes.Contains(result.client, []byte("error")) {
			t.Fatalf("overlapping prompt accepted: %+v, %v", result, err)
		}
		if !bytes.Equal(before, bytes.Join(relayReplayFrames(t, relay), nil)) {
			t.Fatal("rejected prompt erased recovery journal")
		}
	}
	rejected()
	response, _ := relayProviderFrame(t, relay, []byte(`{"id":`+frameID(t, first)+`,"error":{"code":-32603,"message":"failed","data":[1,2]}}`), 1, true)
	if !bytes.Contains(response, []byte(`"providerData":[1,2]`)) || !bytes.Contains(response, []byte(ACPEventIDMetaKey)) {
		t.Fatalf("failed response lost original data or receipt: %s", response)
	}
	rejected()
	ack, _ := json.Marshal(map[string]any{"method": ACPPromptAckMethod, "params": map[string]string{"eventId": relay.snapshot().PendingResultEventID}})
	relayClientFrame(t, relay, ack, 2)
	if next := relayClientFrame(t, relay, prompt, 2); len(next) == 0 {
		t.Fatal("acknowledged prompt still blocked")
	}
}

func TestACPRelayReplacementDoesNotReuseDurableIdentities(t *testing.T) {
	first, replacement := newTestACPRelay(t), newTestACPRelay(t)
	update := []byte(`{"method":"session/update","params":{"_meta":null}}`)
	request := []byte(`{"id":7,"method":"session/request_permission","params":null}`)
	identities := func(relay *acpRelay) [2]string {
		relayClientFrame(t, relay, []byte(`{"id":1,"method":"session/prompt","params":{}}`), 1)
		event, _ := relayProviderFrame(t, relay, update, 1, true)
		permission, _ := relayProviderFrame(t, relay, request, 1, true)
		return [2]string{frameMetaString(t, event, ACPEventIDMetaKey), frameMetaString(t, permission, ACPRequestIDMetaKey)}
	}
	a, b := identities(first), identities(replacement)
	for i := range a {
		if a[i] == "" || b[i] == "" || a[i] == b[i] {
			t.Fatalf("host replacement reused durable identity: %v -> %v", a, b)
		}
	}
	// Attaching another daemon to the same host must retain both identities.
	replay := relayReplayFrames(t, first)
	if len(replay) != 2 || frameMetaString(t, replay[0], ACPEventIDMetaKey) != a[0] ||
		frameMetaString(t, replay[1], ACPRequestIDMetaKey) != a[1] {
		t.Fatalf("reconnect changed durable identities: %q", replay)
	}
}

func TestACPRelayNullPromptResultStillCarriesReceipt(t *testing.T) {
	relay := newTestACPRelay(t)
	prompt := relayClientFrame(t, relay, []byte(`{"id":1,"method":"session/prompt","params":{}}`), 1)
	result, _ := relayProviderFrame(t, relay, []byte(`{"id":`+frameID(t, prompt)+`,"result":null}`), 1, true)
	if !bytes.Contains(result, []byte(relay.snapshot().PendingResultEventID)) || !bytes.Contains(result, []byte(ACPEventIDMetaKey)) {
		t.Fatalf("null result lost completion receipt: %s", result)
	}
}
