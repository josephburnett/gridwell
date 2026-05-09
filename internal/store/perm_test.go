package store

import "testing"

func TestEffective(t *testing.T) {
	cases := []struct {
		name              string
		userID, ownerID, groupID int64
		mode              int32
		inGroup           bool
		wantRead, wantWrite bool
	}{
		{"owner full", 1, 1, 1, 0o600, true, true, true},
		{"owner read only", 1, 1, 1, 0o400, true, true, false},
		{"owner zero (locked from self)", 1, 1, 1, 0o077, false, false, false},
		{"group member rw", 2, 1, 1, 0o060, true, true, true},
		{"group member r only", 2, 1, 1, 0o040, true, true, false},
		{"group not member, other 0", 2, 1, 1, 0o000, false, false, false},
		{"other read", 3, 1, 1, 0o004, false, true, false},
		{"other write", 3, 1, 1, 0o002, false, false, true},
		{"default 0o640 owner", 1, 1, 1, 0o640, true, true, true},
		{"default 0o640 group", 2, 1, 1, 0o640, true, true, false},
		{"default 0o640 other", 3, 1, 1, 0o640, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, w := Effective(c.userID, c.ownerID, c.groupID, c.mode, c.inGroup)
			if r != c.wantRead || w != c.wantWrite {
				t.Errorf("Effective(uid=%d owner=%d group=%d mode=0o%o inGroup=%v) = (r=%v,w=%v), want (%v,%v)",
					c.userID, c.ownerID, c.groupID, c.mode, c.inGroup, r, w, c.wantRead, c.wantWrite)
			}
		})
	}
}
