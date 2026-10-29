//go:build unit

package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnnouncementTargetingRestrictsToExplicitUsers(t *testing.T) {
	target := AnnouncementTargeting{UserIDs: []int64{7, 11}}
	require.True(t, target.MatchesForUser(7, 25, nil))
	require.False(t, target.MatchesForUser(12, 25, nil))
	require.False(t, target.Matches(25, nil), "legacy match without a user ID must not expose a private notice")
}

func TestAnnouncementTargetingCombinesUserAndExistingConditions(t *testing.T) {
	target := AnnouncementTargeting{
		UserIDs: []int64{7},
		AnyOf: []AnnouncementConditionGroup{{AllOf: []AnnouncementCondition{{
			Type: AnnouncementConditionTypeBalance, Operator: AnnouncementOperatorLT, Value: 5,
		}}}},
	}
	require.True(t, target.MatchesForUser(7, 2, nil))
	require.False(t, target.MatchesForUser(7, 8, nil))
	require.False(t, target.MatchesForUser(8, 2, nil))
}

func TestAnnouncementTargetingValidatesExplicitUserIDs(t *testing.T) {
	for _, ids := range [][]int64{{0}, {-1}, {7, 7}} {
		_, err := (AnnouncementTargeting{UserIDs: ids}).NormalizeAndValidate()
		require.Error(t, err, "invalid IDs: %v", ids)
	}

	tooMany := make([]int64, maxAnnouncementTargetUserIDs+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	_, err := (AnnouncementTargeting{UserIDs: tooMany}).NormalizeAndValidate()
	require.Error(t, err)
}
