//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type coworkanyAPIKeyRepo struct {
	APIKeyRepository
	created *APIKey
}

func (r *coworkanyAPIKeyRepo) Create(_ context.Context, key *APIKey) error {
	r.created = key
	return nil
}

type coworkanyUserRepo struct {
	UserRepository
	user *User
}

func (r *coworkanyUserRepo) GetByID(context.Context, int64) (*User, error) {
	return r.user, nil
}

type coworkanyGroupRepo struct {
	GroupRepository
	groups  map[int64]*Group
	err     error
	lookups []int64
}

func (r *coworkanyGroupRepo) GetByID(_ context.Context, id int64) (*Group, error) {
	r.lookups = append(r.lookups, id)
	if r.err != nil {
		return nil, r.err
	}
	group, ok := r.groups[id]
	if !ok {
		return nil, ErrGroupNotFound
	}
	return group, nil
}

type coworkanySubscriptionRepo struct {
	UserSubscriptionRepository
	active  bool
	lookups [][2]int64
}

func (r *coworkanySubscriptionRepo) GetActiveByUserIDAndGroupID(_ context.Context, userID, groupID int64) (*UserSubscription, error) {
	r.lookups = append(r.lookups, [2]int64{userID, groupID})
	if !r.active {
		return nil, ErrSubscriptionNotFound
	}
	return &UserSubscription{UserID: userID, GroupID: groupID}, nil
}

func TestAPIKeyCreateCoworkanyDefaultGroup(t *testing.T) {
	defaultGroupID := int64(47)
	explicitGroupID := int64(83)
	tests := []struct {
		name         string
		keyName      string
		configuredID int64
		groupID      *int64
		group        *Group
		allowed      []int64
		subscription bool
		groupErr     error
		wantGroup    *int64
		wantLookups  []int64
		wantReason   string
		wantStatus   int
	}{
		{
			name: "name-only binds the configured public group", keyName: "Coworkany/device-a",
			configuredID: defaultGroupID, group: &Group{ID: defaultGroupID, Status: StatusActive},
			wantGroup: &defaultGroupID, wantLookups: []int64{defaultGroupID},
		},
		{
			name: "missing configuration fails before creating a key", keyName: "Coworkany/device-a",
			wantReason: "DEFAULT_GROUP_NOT_CONFIGURED", wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "exclusive default requires user permission", keyName: "Coworkany/device-a",
			configuredID: defaultGroupID, group: &Group{ID: defaultGroupID, Status: StatusActive, IsExclusive: true},
			wantLookups: []int64{defaultGroupID}, wantReason: "GROUP_NOT_ALLOWED", wantStatus: http.StatusForbidden,
		},
		{
			name: "authorized exclusive default binds", keyName: "Coworkany/device-a",
			configuredID: defaultGroupID, group: &Group{ID: defaultGroupID, Status: StatusActive, IsExclusive: true},
			allowed: []int64{defaultGroupID}, wantGroup: &defaultGroupID, wantLookups: []int64{defaultGroupID},
		},
		{
			name: "subscription default cannot use allowed-groups permission alone", keyName: "Coworkany/device-a",
			configuredID: defaultGroupID, group: &Group{ID: defaultGroupID, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription},
			allowed: []int64{defaultGroupID}, wantLookups: []int64{defaultGroupID},
			wantReason: "GROUP_NOT_ALLOWED", wantStatus: http.StatusForbidden,
		},
		{
			name: "active subscription authorizes the default", keyName: "Coworkany/device-a",
			configuredID: defaultGroupID, group: &Group{ID: defaultGroupID, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription},
			subscription: true, wantGroup: &defaultGroupID, wantLookups: []int64{defaultGroupID},
		},
		{
			name: "disabled default fails before permission or creation", keyName: "Coworkany/device-a",
			configuredID: defaultGroupID, group: &Group{ID: defaultGroupID, Status: StatusDisabled},
			wantLookups: []int64{defaultGroupID}, wantReason: "DEFAULT_GROUP_UNAVAILABLE", wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "deleted default is explicitly unavailable", keyName: "Coworkany/device-a",
			configuredID: defaultGroupID, wantLookups: []int64{defaultGroupID},
			wantReason: "DEFAULT_GROUP_UNAVAILABLE", wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "repository failure never falls back", keyName: "Coworkany/device-a",
			configuredID: defaultGroupID, groupErr: errors.New("group storage failed"),
			wantLookups: []int64{defaultGroupID}, wantReason: "DEFAULT_GROUP_UNAVAILABLE", wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "explicit group does not require a configured default", keyName: "Coworkany/device-a",
			groupID: &explicitGroupID, group: &Group{ID: explicitGroupID, Status: StatusActive},
			wantGroup: &explicitGroupID, wantLookups: []int64{explicitGroupID},
		},
		{
			name: "explicit group overrides the configured default", keyName: "Coworkany/device-a",
			configuredID: defaultGroupID, groupID: &explicitGroupID, group: &Group{ID: explicitGroupID, Status: StatusActive},
			wantGroup: &explicitGroupID, wantLookups: []int64{explicitGroupID},
		},
		{
			name: "explicit forbidden group keeps existing error", keyName: "Coworkany/device-a",
			configuredID: defaultGroupID, groupID: &explicitGroupID, group: &Group{ID: explicitGroupID, Status: StatusActive, IsExclusive: true},
			wantLookups: []int64{explicitGroupID}, wantReason: "GROUP_NOT_ALLOWED", wantStatus: http.StatusForbidden,
		},
		{
			name: "other clients keep an omitted group", keyName: "Desktop/device-a", configuredID: defaultGroupID,
		},
		{
			name: "bare name does not match the scoped prefix", keyName: "Coworkany", configuredID: defaultGroupID,
		},
		{
			name: "prefix is case-sensitive", keyName: "coworkany/device-a", configuredID: defaultGroupID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := &coworkanyAPIKeyRepo{}
			groups := &coworkanyGroupRepo{groups: make(map[int64]*Group), err: tt.groupErr}
			if tt.group != nil {
				groups.groups[tt.group.ID] = tt.group
			}
			subscriptions := &coworkanySubscriptionRepo{active: tt.subscription}
			user := &User{ID: 29, AllowedGroups: tt.allowed}
			svc := &APIKeyService{
				apiKeyRepo: keys, userRepo: &coworkanyUserRepo{user: user},
				groupRepo: groups, userSubRepo: subscriptions,
				cfg: &config.Config{Coworkany: config.CoworkanyConfig{DefaultGroupID: tt.configuredID}},
			}
			req := CreateAPIKeyRequest{Name: tt.keyName, GroupID: tt.groupID}
			key, err := svc.Create(context.Background(), user.ID, req)
			require.Equal(t, tt.wantLookups, groups.lookups)
			require.Equal(t, tt.groupID, req.GroupID, "creation must not modify the caller's request")
			if tt.wantReason != "" {
				require.Error(t, err)
				require.Equal(t, tt.wantReason, infraerrors.Reason(err))
				require.Equal(t, tt.wantStatus, infraerrors.Code(err))
				require.Nil(t, key)
				require.Nil(t, keys.created, "a failed default binding must not leave an unbound key")
			} else {
				require.NoError(t, err)
				require.NotNil(t, keys.created)
				require.Equal(t, tt.wantGroup, key.GroupID)
				require.Equal(t, tt.wantGroup, keys.created.GroupID)
				require.Equal(t, user.ID, key.UserID)
				require.NotEmpty(t, key.Key)
			}
			if tt.group != nil && tt.group.IsSubscriptionType() && tt.group.IsActive() {
				require.Equal(t, [][2]int64{{user.ID, tt.group.ID}}, subscriptions.lookups)
			} else {
				require.Empty(t, subscriptions.lookups)
			}
		})
	}
}
