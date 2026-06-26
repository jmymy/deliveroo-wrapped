package models

import (
	"encoding/json"
	"testing"
)

// userResponseSample mirrors the shape of a real GET /orderapp/v1/users/{id}
// payload (numeric id, nested subscription) with all personal fields replaced by
// dummy values. It guards against the regression where APIUser.ID was typed
// string: the numeric id made the whole struct fail to decode, so the profile
// (name / Plus tier / member-since) silently never populated after a sync.
const userResponseSample = `{
  "id": 12345678,
  "drn_id": "00000000-0000-0000-0000-000000000000",
  "full_name": "Test User",
  "preferred_name": "Test",
  "created": "2022-04-01T14:13:10.657Z",
  "did_confirm_drinking_age": true,
  "subscription": {
    "active": true,
    "offer_uname": "uk_monthly_2499_2025Q2_no_trial",
    "subscription_tier": "DIAMOND"
  }
}`

func TestAPIUserDecodesNumericID(t *testing.T) {
	var u APIUser
	if err := json.Unmarshal([]byte(userResponseSample), &u); err != nil {
		t.Fatalf("decode APIUser: %v", err)
	}
	if u.ID != 12345678 {
		t.Errorf("ID = %d, want 12345678", u.ID)
	}
	if u.FullName != "Test User" || u.PreferredName != "Test" {
		t.Errorf("name fields: full=%q preferred=%q", u.FullName, u.PreferredName)
	}
	if u.Created != "2022-04-01T14:13:10.657Z" {
		t.Errorf("Created = %q", u.Created)
	}
	if !u.Subscription.Active || u.Subscription.SubscriptionTier != "DIAMOND" {
		t.Errorf("subscription: %+v", u.Subscription)
	}
	if u.Subscription.OfferUname != "uk_monthly_2499_2025Q2_no_trial" {
		t.Errorf("OfferUname = %q", u.Subscription.OfferUname)
	}
}
