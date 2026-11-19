package gameserver

import (
	"testing"

	"rf4server/internal/proto"
)

func TestMarineBottomBaitSlots(t *testing.T) {
	// 4.0.25034：17/2 上报钩 250/251 后，天然饵依次上报 350/351。
	const rt = 55

	s, db := newStaticRigTestServer(t)
	ctx := t.Context()
	const uid = 1000001
	rod, err := s.CreateItem(ctx, uid, "boatr_10222_80_100", "backpack", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.rigSetType(ctx, uid, rod.GUID, rt) {
		t.Fatal("rig type rejected")
	}
	mountArmHook(t, s, rod.GUID, 250)
	mountArmHook(t, s, rod.GUID, 251)
	check := func(first, second string) {
		t.Helper()
		row, err := db.GetItemByGUID(ctx, rod.GUID)
		if err != nil {
			t.Fatal(err)
		}
		stored := rigStoredF5(t, row)
		want := map[int64]string{}
		if first != "" {
			want[350] = first
		}
		if second != "" {
			want[351] = second
		}
		for _, f5 := range []*proto.Value{stored, s.liveRig153(ctx, row, stored)} {
			comps := rigTestCategoryComps(t, f5, catBait)
			if len(comps) != len(want) {
				t.Fatalf("f5 baits = %+v, want %v", comps, want)
			}
			for _, c := range comps {
				if want[c.Extra] != c.GUID || c.Ordinal != c.Extra-350 {
					t.Fatalf("f5 bait = %+v, want %v", c, want)
				}
			}
		}
		rows, err := db.ListRigComponents(ctx, rod.GUID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range rows {
			if r.Slot == catBait {
				n++
				if want[r.Extra] != r.GUID || r.Ordinal != r.Extra-350 {
					t.Fatalf("projection = %+v, want %v", r, want)
				}
			}
		}
		if n != len(want) {
			t.Fatalf("projection has %d baits, want %d", n, len(want))
		}
	}
	source, first := baitTestMount(t, s, rod.GUID, "fb_bf_saida", 4, 350)
	check(first, "")
	mounted := s.rigSetComponent(ctx, uid, rod.GUID, 0, source, 351, rigSetOpts{ParentType: familyRod, EnforceCategory: true, SplitStack: true})
	second := mounted.MountedGUID
	if mounted.Rejected || second == "" || second == first {
		t.Fatal("same stack did not yield a separate second bait")
	}
	check(first, second)
	_, second = baitTestMount(t, s, rod.GUID, "fb_squid", 4, 351)
	s = newFishingRecoveryServer(t, db)
	check(first, second)
	if _, err := s.consumeBaitEvent(ctx, uid, rod.GUID, "first-bite", []string{first}, false); err != nil {
		t.Fatal(err)
	}
	check("", second)
	if _, err := s.reloadBaitForCast(ctx, uid, rod.GUID); err != nil {
		t.Fatal(err)
	}
	first = s.storedRigComponents(ctx, rod.GUID)[rigCompKey(catBait, 0)].GUID
	if first == "" {
		t.Fatal("first bait was not replenished")
	}
	check(first, second)
	if r := s.rigRemoveComponentInstance(ctx, uid, rod.GUID, catBait, "", 351); r == nil || r.CompGUID != second {
		t.Fatalf("detach second: %+v", r)
	}
	check(first, "")
}

// 旧档已保留两份不同实例，但两条 f2 都是 350。按旧数组次序恢复，不改玩家库。
func TestMarineBottomLegacyDuplicateBaitIndex(t *testing.T) {
	const first = "12345678901234567890123456789011"
	const second = "12345678901234567890123456789012"
	old := buildRig153("rig_MarineBottomClassic", 55, nil, 100)
	old.Fields[2].Val.Items = []*proto.Value{
		rigCompObj(catBait, rigComp{GUID: first, Extra: 350}),
		rigCompObj(catBait, rigComp{GUID: second, Extra: 350}),
	}
	rebuilt := buildRig153Like(old, "rig_MarineBottomClassic", 55, rigCompsOf(old), 100, 0)
	items := rebuilt.Fields[2].Val.Items
	if len(items) != 2 || items[0].Fields[1].GuidHex() != first || items[0].Fields[2].U32() != 350 ||
		items[1].Fields[1].GuidHex() != second || items[1].Fields[2].U32() != 351 {
		t.Fatalf("旧档未恢复成两个独立饵位: %+v", items)
	}
}
