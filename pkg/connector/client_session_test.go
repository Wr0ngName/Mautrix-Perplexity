package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

func TestMakeSessionKey(t *testing.T) {
	t.Run("with user MXID returns composite key", func(t *testing.T) {
		client := &PerplexityClient{
			UserLogin: &bridgev2.UserLogin{
				UserLogin: &database.UserLogin{
					UserMXID: id.UserID("@alice:example.com"),
				},
			},
		}

		portalID := networkid.PortalID("portal123")
		sessionKey := client.makeSessionKey(portalID)

		expected := "portal123:@alice:example.com"
		if sessionKey != expected {
			t.Errorf("makeSessionKey() = %q, want %q", sessionKey, expected)
		}
	})

	t.Run("without user MXID returns portal ID only", func(t *testing.T) {
		client := &PerplexityClient{
			UserLogin: &bridgev2.UserLogin{
				UserLogin: &database.UserLogin{
					UserMXID: "",
				},
			},
		}

		portalID := networkid.PortalID("portal456")
		sessionKey := client.makeSessionKey(portalID)

		expected := "portal456"
		if sessionKey != expected {
			t.Errorf("makeSessionKey() = %q, want %q", sessionKey, expected)
		}
	})

	t.Run("with nil UserLogin returns portal ID only", func(t *testing.T) {
		client := &PerplexityClient{
			UserLogin: nil,
		}

		portalID := networkid.PortalID("portal789")
		sessionKey := client.makeSessionKey(portalID)

		expected := "portal789"
		if sessionKey != expected {
			t.Errorf("makeSessionKey() = %q, want %q", sessionKey, expected)
		}
	})

	t.Run("session key format matches Python sidecar", func(t *testing.T) {
		// This test verifies the format matches the Python sidecar's get_session_key()
		// Python: f"{self.portal_id}:{self.user_id}" or just self.portal_id
		client := &PerplexityClient{
			UserLogin: &bridgev2.UserLogin{
				UserLogin: &database.UserLogin{
					UserMXID: id.UserID("@bob:matrix.org"),
				},
			},
		}

		portalID := networkid.PortalID("!room:matrix.org")
		sessionKey := client.makeSessionKey(portalID)

		// Format: portal_id:user_id
		expected := "!room:matrix.org:@bob:matrix.org"
		if sessionKey != expected {
			t.Errorf("makeSessionKey() = %q, want %q (must match Python sidecar format)", sessionKey, expected)
		}

		// Verify colon is the delimiter
		if sessionKey[len("!room:matrix.org")] != ':' {
			t.Error("Session key should use colon as delimiter between portal_id and user_id")
		}
	})

	t.Run("different users get different session keys for same portal", func(t *testing.T) {
		clientAlice := &PerplexityClient{
			UserLogin: &bridgev2.UserLogin{
				UserLogin: &database.UserLogin{
					UserMXID: id.UserID("@alice:example.com"),
				},
			},
		}

		clientBob := &PerplexityClient{
			UserLogin: &bridgev2.UserLogin{
				UserLogin: &database.UserLogin{
					UserMXID: id.UserID("@bob:example.com"),
				},
			},
		}

		portalID := networkid.PortalID("shared_portal")
		aliceKey := clientAlice.makeSessionKey(portalID)
		bobKey := clientBob.makeSessionKey(portalID)

		if aliceKey == bobKey {
			t.Errorf("Different users should get different session keys, both got %q", aliceKey)
		}

		expectedAlice := "shared_portal:@alice:example.com"
		expectedBob := "shared_portal:@bob:example.com"

		if aliceKey != expectedAlice {
			t.Errorf("Alice's key = %q, want %q", aliceKey, expectedAlice)
		}
		if bobKey != expectedBob {
			t.Errorf("Bob's key = %q, want %q", bobKey, expectedBob)
		}
	})
}
