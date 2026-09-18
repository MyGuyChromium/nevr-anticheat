package model

import (
	"fmt"
	"reflect"
)

const DefaultGameRuleProfileID = "echo-defaults-8616ebf03d6f"
const defaultGameRuleRevision = "8616ebf03d6f0f2c1071366d0aad6ee3403e9eb3"
const RuleReferenceOnly = "reference_only_recording_configuration_unverified"

// RuleReference identifies a pinned set of default files, not an authenticated
// match configuration. It cannot construct verified mechanics knowledge.
type RuleReference struct {
	ProfileID      string `json:"profile_id"`
	SourceRevision string `json:"source_revision"`
	Applicability  string `json:"applicability"`
}

func DefaultGameRuleReference() *RuleReference {
	return &RuleReference{DefaultGameRuleProfileID, defaultGameRuleRevision, RuleReferenceOnly}
}

func (r *RuleReference) Validate() error {
	if r == nil {
		return nil
	}
	if *r != *DefaultGameRuleReference() {
		return fmt.Errorf("unsupported game-rule reference or applicability")
	}
	return nil
}

// GameRuleFact preserves literal configuration facts without inventing runtime
// equations, collider transforms, allowed player settings or a throw-speed cap.
type GameRuleFact struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Unit       string `json:"unit,omitempty"`
	SourcePath string `json:"source_path"`
}

type GameRuleProfile struct {
	Reference              RuleReference  `json:"reference"`
	SourceRepository       string         `json:"source_repository"`
	BuildScope             string         `json:"build_scope"`
	RecordingConfiguration string         `json:"recording_configuration"`
	Facts                  []GameRuleFact `json:"facts"`
	Limitations            []string       `json:"limitations"`
}

// Only literal pinned defaults can use this profile identity. Producers cannot
// relabel different values or their own authority claim as the shipped profile.
func (p *GameRuleProfile) Validate() error {
	if p == nil {
		return nil
	}
	if !reflect.DeepEqual(*p, DefaultGameRuleProfile()) {
		return fmt.Errorf("unsupported or modified game-rule profile")
	}
	return nil
}

func (p GameRuleProfile) Clone() GameRuleProfile {
	p.Facts = append([]GameRuleFact(nil), p.Facts...)
	p.Limitations = append([]string(nil), p.Limitations...)
	return p
}

// Returned slices are owned by the caller. Defaults can be shown beside an
// observation, never silently installed as the configuration of that recording.
func DefaultGameRuleProfile() GameRuleProfile {
	const frisbee = "game_config/balance/mp_frisbee_settings.json"
	const punch = "game_config/balance/mp_arena_punch.json"
	const movement = "game_config/balance/mp_arena_movement.json"
	return GameRuleProfile{
		Reference: *DefaultGameRuleReference(), SourceRepository: "thesprockee/nevr-cheat",
		BuildScope:             "The config files do not establish the executable build or platform using them.",
		RecordingConfiguration: "Unknown: this recording has no verified active-config/build binding.",
		Facts: []GameRuleFact{
			{"use_grab_bubble", "true", "", frisbee}, {"grab_range", "0.25", "m", frisbee},
			{"possesion_time", "5.0", "s", frisbee}, {"aim_assist.enable", "true", "", frisbee},
			{"aim_assist.target_choice_half_angle", "22.5", "degrees", frisbee},
			{"aim_assist.min_angle", "10.0", "degrees", frisbee}, {"aim_assist.max_angle", "45.0", "degrees", frisbee},
			{"aim_assist.min_angle_strength", "1.0", "", frisbee}, {"aim_assist.max_angle_strength", "0.3", "", frisbee},
			{"left_hand_radius", "0.06", "m", punch}, {"right_hand_radius", "0.06", "m", punch},
			{"head_hit_radius", "0.15", "m", punch}, {"punch_hit_offset", "0,-0.04,0", "m", punch},
			{"stun_length", "2.0", "s", punch}, {"stun_timing_window", "0.25", "s", punch},
			{"slow_down.slow_down_speed", "4.65", "m/s", movement},
		},
		Limitations: []string{
			"The configured 18.9 m/s review reference is unchanged; these files establish no throw-speed ceiling.",
			"Default assistance parameters do not establish its runtime equation or a permissible wrist-angle setting.",
			"Tracking origins, acquisition/contact timing, latency bounds and active overrides remain unverified.",
			"A default-file match, client assertion or server relay does not grant authority to punish.",
		},
	}
}
