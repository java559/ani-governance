package data

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// This is the private write guard for Gov's existing Volcano v1.15.2 editor.
// It compares the protected configuration within one conditional update. It
// does not produce an Accelerator installation identity or persisted digest.
func instanceSchedulerProtectedProjection(document string) (string, error) {
	if instanceSchedulerVersion != "v1.15.2" {
		return "", fmt.Errorf("scheduler editor version requires a reviewed guard")
	}
	decoder := yaml.NewDecoder(strings.NewReader(document))
	var root, extra yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return "", err
	}
	if err := decoder.Decode(&extra); err != io.EOF || len(root.Content) != 1 {
		return "", fmt.Errorf("scheduler YAML must contain one mapping document")
	}
	// yaml.v3 checks duplicate keys and bounds alias expansion before the
	// projection preserves scalar tags and native YAML 1.1 boolean spelling.
	var checked any
	if err := root.Decode(&checked); err != nil {
		return "", err
	}
	decoded, err := instanceSchedulerYAMLValue(root.Content[0], map[*yaml.Node]bool{})
	if err != nil {
		return "", err
	}
	configuration, ok := decoded.(map[string]any)
	if !ok {
		return "", fmt.Errorf("scheduler document must be a mapping")
	}
	if err := instanceSchedulerProjectScoring(configuration); err != nil {
		return "", err
	}
	value, err := json.Marshal(configuration)
	return string(value), err
}

type instanceSchedulerScalar struct {
	Tag   string
	Value string
}

func instanceSchedulerNativeScalar(node *yaml.Node) instanceSchedulerScalar {
	if node.Tag == "!!str" && node.Style == 0 {
		switch node.Value {
		case "y", "Y", "yes", "Yes", "YES", "on", "On", "ON":
			return instanceSchedulerScalar{"!!bool", "true"}
		case "n", "N", "no", "No", "NO", "off", "Off", "OFF":
			return instanceSchedulerScalar{"!!bool", "false"}
		}
	}
	return instanceSchedulerScalar{node.Tag, node.Value}
}

func instanceSchedulerYAMLValue(node *yaml.Node, visiting map[*yaml.Node]bool) (any, error) {
	if visiting[node] {
		return nil, fmt.Errorf("cyclic scheduler YAML alias")
	}
	visiting[node] = true
	defer delete(visiting, node)
	switch node.Kind {
	case yaml.AliasNode:
		return instanceSchedulerYAMLValue(node.Alias, visiting)
	case yaml.ScalarNode:
		return instanceSchedulerNativeScalar(node), nil
	case yaml.SequenceNode:
		items := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			item, err := instanceSchedulerYAMLValue(child, visiting)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	case yaml.MappingNode:
		values, explicit := map[string]any{}, map[string]bool{}
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || instanceSchedulerNativeScalar(key).Tag != "!!str" && key.Tag != "!!merge" || explicit[key.Value] {
				return nil, fmt.Errorf("scheduler mapping requires unique string keys")
			}
			explicit[key.Value] = true
			value, err := instanceSchedulerYAMLValue(node.Content[index+1], visiting)
			if err != nil {
				return nil, err
			}
			if key.Tag != "!!merge" {
				values[key.Value] = value
				continue
			}
			merges, ok := value.([]any)
			if !ok {
				merges = []any{value}
			}
			for _, merge := range merges {
				mapping, ok := merge.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("scheduler YAML merge requires mappings")
				}
				for name, item := range mapping {
					if _, exists := values[name]; !exists {
						values[name] = item
					}
				}
			}
		}
		return values, nil
	default:
		return nil, fmt.Errorf("unsupported scheduler YAML node")
	}
}

func instanceSchedulerScalarType(value any, tag string) error {
	scalar, ok := value.(instanceSchedulerScalar)
	if !ok || scalar.Tag != tag {
		return fmt.Errorf("invalid native scheduler scalar type")
	}
	if tag == "!!int" {
		var number int
		if err := (&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: scalar.Value}).Decode(&number); err != nil {
			return err
		}
	}
	return nil
}

func instanceSchedulerScalarText(value any) string {
	if scalar, ok := value.(instanceSchedulerScalar); ok && scalar.Tag == "!!str" {
		return scalar.Value
	}
	return ""
}

func instanceSchedulerProjectScoring(configuration map[string]any) error {
	if actions, exists := configuration["actions"]; exists {
		if err := instanceSchedulerScalarType(actions, "!!str"); err != nil {
			return err
		}
	}
	if value, exists := configuration["metrics"]; exists {
		metrics, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("scheduler metrics must be a mapping")
		}
		for _, metric := range metrics {
			if err := instanceSchedulerScalarType(metric, "!!str"); err != nil {
				return err
			}
		}
	}
	if value, exists := configuration["configurations"]; exists {
		entries, ok := value.([]any)
		if !ok {
			return fmt.Errorf("scheduler configurations must be a sequence")
		}
		for _, entry := range entries {
			item, ok := entry.(map[string]any)
			if !ok || instanceSchedulerScalarText(item["name"]) == "" {
				return fmt.Errorf("scheduler configuration requires a name")
			}
			if arguments, exists := item["arguments"]; exists {
				if _, ok := arguments.(map[string]any); !ok {
					return fmt.Errorf("scheduler configuration arguments must be a mapping")
				}
			}
		}
	}
	value, exists := configuration["tiers"]
	if !exists {
		return nil
	}
	tiers, ok := value.([]any)
	if !ok {
		return fmt.Errorf("scheduler tiers must be a sequence")
	}
	keptTiers, dropped := []any{}, false
	for _, value := range tiers {
		tier, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("scheduler tier must be a mapping")
		}
		value, exists := tier["plugins"]
		if !exists {
			keptTiers = append(keptTiers, tier)
			continue
		}
		plugins, ok := value.([]any)
		if !ok {
			return fmt.Errorf("scheduler plugins must be a sequence")
		}
		keptPlugins, tierDropped := []any{}, false
		for _, value := range plugins {
			plugin, ok := value.(map[string]any)
			if !ok || instanceSchedulerScalarText(plugin["name"]) == "" {
				return fmt.Errorf("scheduler plugin requires a name")
			}
			remove, err := instanceSchedulerProjectPlugin(plugin)
			if err != nil {
				return err
			}
			if remove {
				tierDropped = true
			} else {
				keptPlugins = append(keptPlugins, plugin)
			}
		}
		if tierDropped && len(keptPlugins) == 0 && len(tier) == 1 {
			dropped = true
			continue
		}
		tier["plugins"] = keptPlugins
		keptTiers = append(keptTiers, tier)
	}
	if dropped && len(keptTiers) == 0 {
		delete(configuration, "tiers")
	} else {
		configuration["tiers"] = keptTiers
	}
	return nil
}

func instanceSchedulerProjectPlugin(plugin map[string]any) (bool, error) {
	name := instanceSchedulerScalarText(plugin["name"])
	arguments := map[string]any{}
	if value, exists := plugin["arguments"]; exists {
		var ok bool
		arguments, ok = value.(map[string]any)
		if !ok {
			return false, fmt.Errorf("scheduler plugin arguments must be a mapping")
		}
	}
	for _, flag := range []string{"enableJobOrder", "enableHierarchy", "enableJobReady", "enableJobPipelined", "enableTaskOrder", "enablePreemptable", "enableReclaimable", "enablePreemptive", "enableQueueOrder", "EnabledClusterOrder", "enablePredicate", "enableBestNode", "enableNodeOrder", "enableTargetJob", "enableReservedNodes", "enableJobEnqueued", "enabledVictim", "enableJobStarving", "enabledOverused", "enabledAllocatable", "enabledHyperNodeOrder", "enabledSubJobReady", "enabledSubJobPipelined", "enabledSubJobOrder", "enabledHyperNodeGradient"} {
		if value, exists := plugin[flag]; exists {
			if err := instanceSchedulerScalarType(value, "!!bool"); err != nil {
				return false, err
			}
		}
	}
	if name == "deviceshare" {
		for _, flag := range []string{"deviceshare.GPUSharingEnable", "deviceshare.GPUNumberEnable", "deviceshare.VGPUEnable", "deviceshare.NodeLockEnable", "deviceshare.AscendMindClusterVNPUEnable", "deviceshare.AscendHAMiVNPUEnable"} {
			if value, exists := arguments[flag]; exists {
				if err := instanceSchedulerScalarType(value, "!!bool"); err != nil {
					return false, err
				}
			}
		}
		for _, field := range []string{"deviceshare.KnownGeometriesCMName", "deviceshare.KnownGeometriesCMNamespace"} {
			if value, exists := arguments[field]; exists {
				if err := instanceSchedulerScalarType(value, "!!str"); err != nil {
					return false, err
				}
			}
		}
	}
	if name == "nodeorder" || name == "binpack" || name == "deviceshare" {
		delete(plugin, "enableNodeOrder")
	}
	resources := map[string]bool{}
	if name == "binpack" {
		if value, exists := arguments["binpack.resources"]; exists {
			if err := instanceSchedulerScalarType(value, "!!str"); err != nil {
				return false, err
			}
			for _, resource := range strings.Split(instanceSchedulerScalarText(value), ",") {
				if resource = strings.TrimSpace(resource); resource != "" {
					resources["binpack.resources."+resource] = true
				}
			}
		}
	}
	for field, value := range arguments {
		tag := ""
		switch name {
		case "nodeorder":
			switch field {
			case "nodeaffinity.weight", "podaffinity.weight", "leastrequested.weight", "mostrequested.weight", "balancedresource.weight", "tainttoleration.weight", "imagelocality.weight", "podtopologyspread.weight":
				tag = "!!int"
			}
		case "binpack":
			if field == "binpack.resources" {
				tag = "!!str"
			} else if field == "binpack.weight" || field == "binpack.cpu" || field == "binpack.memory" || resources[field] {
				tag = "!!int"
			}
		case "deviceshare":
			if field == "deviceshare.SchedulePolicy" {
				tag = "!!str"
			} else if field == "deviceshare.ScheduleWeight" {
				tag = "!!int"
			}
		}
		if tag != "" {
			if err := instanceSchedulerScalarType(value, tag); err != nil {
				return false, err
			}
			delete(arguments, field)
		}
	}
	if len(arguments) == 0 {
		delete(plugin, "arguments")
	}
	return (name == "nodeorder" || name == "binpack") && len(plugin) == 1, nil
}
