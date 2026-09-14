package banner

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const decidedAt = 1788944400

// fixture decodes one of the shared wire fixtures, which are what the backend
// publisher is developed against. A field this package stops reading, or a
// field it starts requiring, breaks here rather than in production.
func fixture(t *testing.T, name string) Command {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".json")
	require.NoError(t, err)
	var cmd Command
	require.NoError(t, json.Unmarshal(raw, &cmd))
	return cmd
}

// valid is a command every field of which passes, so a case can break exactly
// one thing and name what it broke.
func valid(t *testing.T) Command {
	t.Helper()
	return fixture(t, "materialize")
}

func TestFixturesAreTheContract(t *testing.T) {
	t.Run("a materialize carries its decision and every locale of it", func(t *testing.T) {
		cmd := fixture(t, "materialize")

		assert.Equal(t, "alice.twake.app", cmd.WorkplaceFqdn)
		assert.Empty(t, cmd.OrgID)
		assert.Equal(t, "banner-command-42", cmd.EventID)
		assert.Equal(t, int64(42), cmd.Revision)
		assert.Equal(t, int64(decidedAt), cmd.Timestamp)
		assert.Equal(t, CategoryBilling, cmd.Category)
		assert.Equal(t, "billing.grace.cycle-a.attempt-2", cmd.BannerID)
		assert.Equal(t, SeverityWarning, cmd.Severity)
		assert.Equal(t, SurfaceBanner, cmd.Surface)
		assert.Equal(t, 150, cmd.Priority)
		assert.True(t, cmd.Dismissible)
		assert.Equal(t, "Échec du paiement", cmd.Title["fr"])
		require.NotNil(t, cmd.CTA)
		assert.Equal(t, "Mettre à jour le moyen de paiement", cmd.CTA.Label["fr"])
		require.NotNil(t, cmd.SecondaryCTA)
		require.NotNil(t, cmd.StartsAt)
		require.NotNil(t, cmd.EndsAt)
		assert.NoError(t, cmd.validate())
	})

	t.Run("a clear carries no wording", func(t *testing.T) {
		cmd := fixture(t, "clear")
		cmd.Clear = true

		assert.Empty(t, cmd.BannerID)
		assert.Empty(t, cmd.Text)
		assert.Equal(t, int64(43), cmd.Revision)
		assert.NoError(t, cmd.validate())
	})

	t.Run("an organization is addressed by its org ID", func(t *testing.T) {
		cmd := fixture(t, "organization")

		assert.Equal(t, "acme_org:123", cmd.OrgID)
		assert.Empty(t, cmd.WorkplaceFqdn)
		assert.Equal(t, SurfaceModal, cmd.Surface)
		assert.NoError(t, cmd.validate())
	})

	t.Run("the payload cannot smuggle document fields", func(t *testing.T) {
		raw := []byte(`{
			"category": "billing", "workplaceFqdn": "alice.twake.app",
			"revision": 1, "timestamp": 1788944400,
			"bannerId": "billing.restricted", "severity": "error", "surface": "banner",
			"text": {"en": "restricted"},
			"_id": "banner-billing", "_rev": "9-forged", "clear": true,
			"dismissedAt": "2026-01-01T00:00:00Z",
			"cozyMetadata": {"createdByApp": "drive"}
		}`)
		var cmd Command
		require.NoError(t, json.Unmarshal(raw, &cmd))

		assert.False(t, cmd.Clear, "only the routing key decides a clear")
		b := cmd.banner("en")
		require.NotNil(t, b)
		assert.Empty(t, b.DocID)
		assert.Empty(t, b.DocRev)
		assert.Nil(t, b.DismissedAt)
		assert.Nil(t, b.Metadata)
	})
}

// TestValidateRejections has one case per rejection: a missing case is a
// hole, not a gap in coverage.
func TestValidateRejections(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(*Command)
		want   string
	}{
		{"no category", func(c *Command) { c.Category = "" }, "not a valid category"},
		{"category with an upper case letter", func(c *Command) { c.Category = "Billing" }, "not a valid category"},
		{"category starting with a digit", func(c *Command) { c.Category = "2fa" }, "not a valid category"},
		{"category too long", func(c *Command) { c.Category = strings.Repeat("a", 33) }, "not a valid category"},
		{"the quota category", func(c *Command) { c.Category = CategoryQuota }, "reserved for the stack's own rules"},
		{"no target", func(c *Command) { c.WorkplaceFqdn = "" }, "exactly one of orgId and workplaceFqdn"},
		{"both targets", func(c *Command) { c.OrgID = "acme.example" }, "exactly one of orgId and workplaceFqdn"},
		{"orgId too long", func(c *Command) { c.WorkplaceFqdn = ""; c.OrgID = strings.Repeat("a", maxOrgIDLen+1) }, "orgId must be at most"},
		{"blank orgId", func(c *Command) { c.WorkplaceFqdn = ""; c.OrgID = " " }, "no surrounding whitespace"},
		{"orgId with surrounding whitespace", func(c *Command) { c.WorkplaceFqdn = ""; c.OrgID = " acme" }, "no surrounding whitespace"},
		{"a target with a path", func(c *Command) { c.WorkplaceFqdn = "alice.twake.app/../bob" }, "is not a valid target"},
		{"a target with a scheme", func(c *Command) { c.WorkplaceFqdn = "https://alice.twake.app" }, "is not a valid target"},
		{"a target too long", func(c *Command) { c.WorkplaceFqdn = strings.Repeat("a", 256) }, "is not a valid target"},
		{"no revision", func(c *Command) { c.Revision = 0 }, "a positive revision is required"},
		{"a negative revision", func(c *Command) { c.Revision = -1 }, "a positive revision is required"},
		{"no timestamp", func(c *Command) { c.Timestamp = 0 }, "timestamp is required"},
		{"timestamp in milliseconds", func(c *Command) { c.Timestamp *= 1000 }, "timestamp must be epoch seconds"},
		{"event id too long", func(c *Command) { c.EventID = strings.Repeat("e", maxEventIDLen+1) }, "eventId is longer than"},
		{"no bannerId", func(c *Command) { c.BannerID = "" }, "not a valid identifier"},
		{"bannerId with a slash", func(c *Command) { c.BannerID = "billing/grace" }, "not a valid identifier"},
		{"bannerId too long", func(c *Command) { c.BannerID = strings.Repeat("a", 65) }, "not a valid identifier"},
		{"unknown severity", func(c *Command) { c.Severity = "critical" }, "severity"},
		{"no severity", func(c *Command) { c.Severity = "" }, "severity"},
		{"unknown surface", func(c *Command) { c.Surface = "toast" }, "surface"},
		{"no surface", func(c *Command) { c.Surface = "" }, "surface"},
		{"a negative priority", func(c *Command) { c.Priority = -1 }, "priority"},
		{"a priority above the range", func(c *Command) { c.Priority = maxPriority + 1 }, "priority"},
		{"a window that ends before it starts", func(c *Command) { c.StartsAt, c.EndsAt = c.EndsAt, c.StartsAt }, "startsAt is not before endsAt"},
		{"a window with no length", func(c *Command) { c.EndsAt = c.StartsAt }, "startsAt is not before endsAt"},
		{"window outside RFC3339", func(c *Command) { at := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC); c.EndsAt = &at }, "window must be within"},
		{"no text", func(c *Command) { c.Text = nil }, "required in the en fallback locale"},
		{"text without the fallback locale", func(c *Command) { delete(c.Text, "en") }, "required in the en fallback locale"},
		{"a title without the fallback locale", func(c *Command) { delete(c.Title, "en") }, "required in the en fallback locale"},
		{"a cta label without the fallback locale", func(c *Command) { delete(c.CTA.Label, "en") }, "required in the en fallback locale"},
		{"text too long", func(c *Command) { c.Text["en"] = strings.Repeat("a", maxTextLen+1) }, "text is longer than"},
		{"locale key too long", func(c *Command) { c.Text[strings.Repeat("a", maxLocaleLen+1)] = "text" }, "locale key must be"},
		{"empty locale key", func(c *Command) { c.Text[""] = "text" }, "locale key must be"},
		{"more locales than a document could ever read", func(c *Command) {
			for i := 0; i <= maxLocales; i++ {
				c.Text[fmt.Sprintf("l%d", i)] = "filler"
			}
		}, "carries more than"},
		{"text too long in another locale", func(c *Command) { c.Text["fr"] = strings.Repeat("a", maxTextLen+1) }, "text is longer than"},
		{"title too long", func(c *Command) { c.Title["en"] = strings.Repeat("a", maxTitleLen+1) }, "title is longer than"},
		{"cta over http", func(c *Command) { c.CTA.URL = "http://manager.example.org" }, "cta.url is not an absolute https URL"},
		{"cta with a javascript scheme", func(c *Command) { c.CTA.URL = "javascript:alert(1)" }, "cta.url is not an absolute https URL"},
		{"cta with a relative url", func(c *Command) { c.CTA.URL = "/billing" }, "cta.url is not an absolute https URL"},
		{"cta with no url", func(c *Command) { c.CTA.URL = "" }, "cta.url is not an absolute https URL"},
		{"cta url too long", func(c *Command) { c.CTA.URL = "https://manager.example.org/" + strings.Repeat("a", maxURLLen) }, "cta.url is longer than"},
		{"cta label too long", func(c *Command) { c.CTA.Label["en"] = strings.Repeat("a", maxLabelLen+1) }, "cta.label is longer than"},
		{"secondary cta over http", func(c *Command) { c.SecondaryCTA.URL = "http://twake.app/support" }, "secondaryCta.url is not an absolute https URL"},
		{"a secondary action with no primary", func(c *Command) { c.CTA = nil }, "secondaryCta needs a cta"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := valid(t)
			tc.break_(&cmd)
			err := cmd.validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidCommand, "the failure has to be classified as unfixable")
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("an end stated without a start is not judged here", func(t *testing.T) {
		// The start comes from the stored occurrence, which validation cannot
		// see, so the pair is checked once the merge has resolved it.
		for _, name := range []string{"an end before the decision", "an end at the decision"} {
			cmd := valid(t)
			at := time.Unix(cmd.Timestamp, 0).UTC()
			if name == "an end before the decision" {
				at = at.Add(-24 * time.Hour)
			}
			cmd.StartsAt, cmd.EndsAt = nil, &at
			assert.NoError(t, cmd.validate(), name)
		}
	})

	t.Run("a complete command is accepted", func(t *testing.T) {
		assert.NoError(t, valid(t).validate())
	})

	t.Run("a clear is still addressed and ordered", func(t *testing.T) {
		clear := func() Command {
			cmd := fixture(t, "clear")
			cmd.Clear = true
			return cmd
		}
		for _, tc := range []struct {
			name   string
			break_ func(*Command)
			want   string
		}{
			{"no revision", func(c *Command) { c.Revision = 0 }, "a positive revision is required"},
			{"no timestamp", func(c *Command) { c.Timestamp = 0 }, "timestamp is required"},
			{"timestamp in milliseconds", func(c *Command) { c.Timestamp *= 1000 }, "timestamp must be epoch seconds"},
			{"oversized event id", func(c *Command) { c.EventID = strings.Repeat("e", maxEventIDLen+1) }, "eventId is longer than"},
			{"oversized wording", func(c *Command) { c.Text = Localized{"en": strings.Repeat("x", 2<<20)} }, "clear must not carry presentation"},
			{"ordinary wording", func(c *Command) { c.Text = Localized{"en": "ignored?"} }, "clear must not carry presentation"},
			{"no target", func(c *Command) { c.WorkplaceFqdn = "" }, "exactly one of orgId and workplaceFqdn"},
			{"the quota category", func(c *Command) { c.Category = CategoryQuota }, "reserved"},
		} {
			cmd := clear()
			tc.break_(&cmd)
			assert.ErrorContains(t, cmd.validate(), tc.want, tc.name)
		}
	})
}

func TestCommandDocumentShape(t *testing.T) {
	at := time.Unix(decidedAt, 0).UTC()

	t.Run("every field of the contract", func(t *testing.T) {
		b := valid(t).banner("en")
		require.NotNil(t, b)
		assert.Equal(t, "billing.grace.cycle-a.attempt-2", b.BannerID)
		assert.Equal(t, CategoryBilling, b.Category)
		assert.Equal(t, SeverityWarning, b.Severity)
		assert.Equal(t, SurfaceBanner, b.Surface)
		assert.True(t, b.Dismissible)
		assert.Equal(t, 150, b.Priority)
		assert.Equal(t, "Payment failed", b.Title)
		require.NotNil(t, b.SecondaryCTA)
		assert.Equal(t, "https://twake.app/support", b.SecondaryCTA.URL)
		assert.Equal(t, TriggerCommand, b.Source.Trigger)
		assert.Equal(t, at, b.Source.At, "the decision time is provenance on the document")
		require.NotNil(t, b.StartsAt)
		require.NotNil(t, b.EndsAt)
	})

	t.Run("a window the backend left out is left for Materialize to fill", func(t *testing.T) {
		cmd := valid(t)
		cmd.StartsAt, cmd.EndsAt = nil, nil
		b := cmd.banner("en")
		require.NotNil(t, b)
		assert.Nil(t, b.StartsAt, "an unstated window keeps the occurrence's own start")
		assert.Nil(t, b.EndsAt)
		assert.Equal(t, at, b.Source.At, "and Materialize starts a first one at the decision")
	})

	t.Run("a clear produces an expired ordering record", func(t *testing.T) {
		cmd := valid(t)
		cmd.Clear = true
		b := cmd.banner("en")
		require.NotNil(t, b)
		assert.True(t, b.Cleared)
		require.NotNil(t, b.EndsAt)
		assert.True(t, b.EndsAt.Before(time.Now()))
		assert.Nil(t, b.Accepted)
	})
}

func TestLocaleIsPickedForTheWholeBanner(t *testing.T) {
	t.Run("the instance locale when the backend sent all of it", func(t *testing.T) {
		b := valid(t).banner("fr")
		require.NotNil(t, b)
		assert.Equal(t, "Échec du paiement", b.Title)
		assert.Contains(t, b.Text, "Nous n'avons pas pu")
		assert.Equal(t, "Mettre à jour le moyen de paiement", b.CTA.Label)
		assert.Equal(t, "fr", b.Lang)
	})

	t.Run("the fallback locale when the backend sent none of it", func(t *testing.T) {
		b := valid(t).banner("de")
		require.NotNil(t, b)
		assert.Equal(t, "Payment failed", b.Title)
		assert.Equal(t, "en", b.Lang, "lang names the language the user actually reads")
	})

	t.Run("an instance with no locale reads the fallback", func(t *testing.T) {
		b := valid(t).banner("")
		require.NotNil(t, b)
		assert.Equal(t, "en", b.Lang)
	})

	t.Run("a language the stack has no catalog for is still the backend's to send", func(t *testing.T) {
		require.NotContains(t, consts.SupportedLocales, "ru")
		cmd := valid(t)
		cmd.Text["ru"] = "Мы не смогли списать средства с вашей карты."
		cmd.Title["ru"] = "Платёж не прошёл"
		cmd.CTA.Label["ru"] = "Обновить способ оплаты"
		cmd.SecondaryCTA.Label["ru"] = "Связаться со службой поддержки"

		b := cmd.banner("ru")
		require.NotNil(t, b)
		assert.Equal(t, "ru", b.Lang, "the stack renders none of this, so its catalogs have no say")
		assert.Equal(t, "Платёж не прошёл", b.Title)
	})

	t.Run("a half translated locale is not used at all", func(t *testing.T) {
		for _, missing := range []func(*Command){
			func(c *Command) { delete(c.Text, "fr") },
			func(c *Command) { delete(c.Title, "fr") },
			func(c *Command) { delete(c.CTA.Label, "fr") },
			func(c *Command) { delete(c.SecondaryCTA.Label, "fr") },
		} {
			cmd := valid(t)
			missing(&cmd)
			b := cmd.banner("fr")
			require.NotNil(t, b)
			assert.Equal(t, "en", b.Lang, "a sentence and its button must be in one language")
			assert.Equal(t, "Payment failed", b.Title)
		}
	})
}
