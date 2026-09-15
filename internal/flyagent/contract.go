package flyagent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	outputMoveLeft     = "move_left"
	outputMoveRight    = "move_right"
	outputMoveUp       = "move_up"
	outputMoveDown     = "move_down"
	outputMoveForward  = "move_forward"
	outputMoveBackward = "move_backward"
	outputYawLeft      = "yaw_left"
	outputYawRight     = "yaw_right"
	outputGripLeft     = "grip_left"
	outputGripRight    = "grip_right"
	outputRelease      = "release"
	outputBoost        = "boost"
	outputBrake        = "brake"
)

var requiredMotorPopulations = []string{
	outputMoveLeft, outputMoveRight, outputMoveUp, outputMoveDown,
	outputMoveForward, outputMoveBackward, outputYawLeft, outputYawRight,
	outputGripLeft, outputGripRight, outputRelease, outputBoost, outputBrake,
}

var actionableSensoryChannels = func() map[string]struct{} {
	names := []string{
		"has_disc", "disc_free", "shield", "target_is_disc", "target_is_goal",
		"disc_approaching", "opponent_closing", "grab_opportunity", "throw_opportunity",
		"boost_opportunity", "brake_opportunity", "evade_left", "evade_right",
	}
	for _, prefix := range []string{"target", "opponent", "teammate"} {
		for _, suffix := range []string{"left", "right", "up", "down", "ahead", "behind", "near", "far"} {
			names = append(names, prefix+"_"+suffix)
		}
	}
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}()

var encodedSensoryChannels = func() map[string]struct{} {
	result := make(map[string]struct{}, len(actionableSensoryChannels)+12)
	for name := range actionableSensoryChannels {
		result[name] = struct{}{}
	}
	for _, name := range []string{
		"game_active", "phase_known", "orientation_known", "orientation_missing",
		"self_velocity_known", "self_velocity_missing", "team_known",
		"possession_known", "possession_conflict", "stunned",
		"disc_missing", "opponent_missing", "teammate_missing", "target_missing",
	} {
		result[name] = struct{}{}
	}
	return result
}()

// ValidateAgentContract ensures a generic graph is wired to the stable Echo
// sensor/motor interface before the action decoder is allowed to use it.
func ValidateAgentContract(topology *Topology) error {
	if topology == nil {
		return errors.New("agent topology is nil")
	}
	if err := topology.Validate(); err != nil {
		return err
	}
	var recognizedInputs []string
	for name := range topology.Inputs {
		if _, ok := encodedSensoryChannels[name]; ok {
			recognizedInputs = append(recognizedInputs, name)
		}
	}
	if len(recognizedInputs) == 0 {
		available := make([]string, 0, len(topology.Inputs))
		for name := range topology.Inputs {
			available = append(available, name)
		}
		sort.Strings(available)
		return fmt.Errorf("agent topology has no recognized sensory input population (found %s)", strings.Join(available, ", "))
	}
	var missing []string
	for _, name := range requiredMotorPopulations {
		if len(topology.Outputs[name]) == 0 {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("agent topology is missing required motor populations: %s", strings.Join(missing, ", "))
	}
	reachable := make(map[string]bool, len(topology.Nodes))
	queue := make([]string, 0)
	for name, members := range topology.Inputs {
		if _, actionable := actionableSensoryChannels[name]; !actionable {
			continue
		}
		for _, member := range members {
			if !reachable[member] {
				reachable[member] = true
				queue = append(queue, member)
			}
		}
	}
	if len(queue) == 0 {
		return errors.New("agent topology has no actionable sensory input population")
	}
	adjacency := make(map[string][]string)
	for _, edge := range topology.Edges {
		adjacency[edge.Pre] = append(adjacency[edge.Pre], edge.Post)
	}
	for len(queue) > 0 {
		pre := queue[0]
		queue = queue[1:]
		for _, post := range adjacency[pre] {
			if !reachable[post] {
				reachable[post] = true
				queue = append(queue, post)
			}
		}
	}
	missing = missing[:0]
	for _, name := range requiredMotorPopulations {
		populationReachable := false
		for _, member := range topology.Outputs[name] {
			populationReachable = populationReachable || reachable[member]
		}
		if !populationReachable {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("agent topology has no actionable input path to motor populations: %s", strings.Join(missing, ", "))
	}
	return nil
}
