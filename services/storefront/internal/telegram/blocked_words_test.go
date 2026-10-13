package telegram

import "testing"

func TestBlockedWordMatching(t *testing.T) {
	words := []string{"nft", "solana", "mint", "sol", "加群推广", "free gift"}
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"NFT giveaway", true}, {"buy $SOL!", true}, {"#SoLaNa", true},
		{"ＭＩＮＴ now", true}, {"买SOL", true}, {"nft.example", true},
		{"请加群推广", true}, {"a FREE GIFT today", true}, {"solution console minting", false},
		{"solana2 NFT_collection", false}, {"", false}, {"正常交流", false},
		{"solution sol", true},
	} {
		if got := containsBlockedWord(tc.text, words); got != tc.want {
			t.Errorf("match %q = %v, want %v", tc.text, got, tc.want)
		}
	}
}
