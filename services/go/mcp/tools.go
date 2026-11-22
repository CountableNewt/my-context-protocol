package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	skillruntime "github.com/CountableNewt/my-context-protocol/services/go/runtime"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

func marshalValue(value any) string { raw, _ := json.Marshal(value); return string(raw) }
func (s *Service) callTool(ctx context.Context, snapshot skillruntime.Snapshot, p Principal, params rpcParams, version string) (any, int, string, []Observation) {
	args := params.Arguments
	if args == nil {
		args = map[string]json.RawMessage{}
	}
	name := params.Name
	var value any
	var err error
	additional := []any{}
	observations := []Observation{}
	textOutput := ""
	switch name {
	case "resolve_context", "discover_skills", "list_capabilities", "mycontext_catalog":
		normalized := map[string]json.RawMessage{}
		for k, v := range args {
			normalized[k] = v
		}
		if name != "resolve_context" {
			request := "List the project skills relevant to the current task"
			if name == "discover_skills" {
				request, _ = rawString(args, "query", 65536, false)
				if request == "" {
					request = "Discover the project skills relevant to the current task"
				}
			} else if name == "mycontext_catalog" {
				skill, _ := rawString(args, "skill", 128, false)
				mode, _ := rawString(args, "mode", 32, false)
				if skill != "" || strings.EqualFold(mode, "skill") {
					id := strings.TrimPrefix(skill, "ctx://skill/")
					id, _ = url.PathUnescape(id)
					getArgs := map[string]json.RawMessage{}
					for key, value := range args {
						getArgs[key] = value
					}
					getArgs["skill_id"] = json.RawMessage(strconv.Quote(id))
					output, code, message, observations := s.callTool(ctx, snapshot, p, rpcParams{Name: "get_skill", Arguments: getArgs}, version)
					if code == 0 {
						if result, ok := output.(map[string]any); ok && result["isError"] == false {
							if content, ok := result["content"].([]any); ok && len(content) > 0 {
								if first, ok := content[0].(map[string]any); ok {
									if raw, ok := first["text"].(string); ok {
										var skill map[string]any
										if json.Unmarshal([]byte(raw), &skill) == nil {
											title, _ := skill["name"].(string)
											if title == "" {
												title = id
											}
											instructions, _ := skill["instructions"].(string)
											if instructions != "" {
												first["text"] = "# " + title + "\n\n" + instructions
											}
										}
									}
								}
							}
						}
					}
					return output, code, message, observations
				}
				if task, _ := rawString(args, "task", 65536, false); task != "" {
					request = task
				}
			} else if skill, _ := rawString(args, "skill_id", 128, false); skill != "" {
				request = skill
				normalized["current_skill_ids"] = json.RawMessage("[" + strconv.Quote(skill) + "]")
			}
			normalized["request"] = json.RawMessage(strconv.Quote(request))
		}
		input, e := skillruntime.DecodeResolve(normalized)
		if e != nil {
			return nil, -32602, e.Error(), nil
		}
		resolution, e := skillruntime.Resolve(snapshot, input)
		err = e
		value = resolution
		for _, step := range resolution.ResolutionTrace {
			var selected *skillruntime.Skill
			for i := range snapshot.Skills {
				if snapshot.Skills[i].SkillID == step.SkillID {
					selected = &snapshot.Skills[i]
					break
				}
			}
			if selected != nil {
				kind := "resolver_excluded"
				if step.Outcome == "selected" {
					kind = "resolver_suggested"
					for _, active := range resolution.ActiveSkills {
						if active.ID == step.SkillID {
							kind = "resolver_selected"
						}
					}
				}
				observations = append(observations, Observation{Skill: selected, TraceID: resolution.TraceID, Type: kind, Source: name, Reason: step.Reason})
			}
		}
		observations = append(observations, Observation{TraceID: resolution.TraceID, Type: "resolution_trace", Source: name})
		if name != "list_capabilities" {
			for _, selected := range append(append([]skillruntime.ResolvedSkill{}, resolution.ActiveSkills...), resolution.SuggestedTaskSkills...) {
				skill, _ := snapshot.FindSkill(selected.ID, selected.Version)
				observations = append(observations, Observation{Skill: skill, TraceID: resolution.TraceID, Type: "surfaced", Source: name})
				if selected.ContentIncluded {
					observations = append(observations, Observation{Skill: skill, TraceID: resolution.TraceID, Type: "instructions_delivered", Source: name})
				}
			}
		}
		if name == "list_capabilities" {
			known := map[string]bool{}
			for _, binding := range resolution.CapabilityBindings {
				known[binding.Capability] = true
			}
			names := []string{}
			for cap := range known {
				names = append(names, cap)
			}
			sortStrings(names)
			value = map[string]any{"schemaVersion": 1, "knownCapabilities": names, "bindings": resolution.CapabilityBindings, "unresolvedRequirements": resolution.MissingRequirements}
		}
		if name == "mycontext_catalog" {
			mode, _ := rawString(args, "mode", 32, false)
			task, _ := rawString(args, "task", 65536, false)
			if strings.EqualFold(mode, "route") || task != "" {
				limit := 5
				if raw, ok := args["limit"]; ok {
					var number int
					if json.Unmarshal(raw, &number) == nil {
						limit = number
					} else {
						var str string
						if json.Unmarshal(raw, &str) == nil {
							if n, e := strconv.Atoi(str); e == nil {
								limit = n
							}
						}
					}
				}
				limit = max(1, min(20, limit))
				resolution.ActiveSkills = resolution.ActiveSkills[:min(limit, len(resolution.ActiveSkills))]
				resolution.SuggestedTaskSkills = resolution.SuggestedTaskSkills[:min(max(0, limit-len(resolution.ActiveSkills)), len(resolution.SuggestedTaskSkills))]
				selectedIDs := map[string]bool{}
				for _, skill := range append(append([]skillruntime.ResolvedSkill{}, resolution.ActiveSkills...), resolution.SuggestedTaskSkills...) {
					selectedIDs[skill.ID] = true
				}
				visibleObservations := []Observation{}
				for _, observation := range observations {
					delivery := observation.Type == "surfaced" || observation.Type == "instructions_delivered"
					if !delivery || observation.Skill != nil && selectedIDs[observation.Skill.SkillID] {
						visibleObservations = append(visibleObservations, observation)
					}
				}
				observations = visibleObservations
				filtered := []skillruntime.NextAction{}
				for _, action := range resolution.NextActions {
					if action.SkillID == "" || selectedIDs[action.SkillID] {
						filtered = append(filtered, action)
					}
				}
				resolution.NextActions = filtered
				value = resolution
				textOutput = "# MCP catalog route\n\nResolved by `resolve_context`.\n\n```json\n" + marshalValue(value) + "\n```"
			} else {
				textOutput = "# MCP catalog\n\nResolved by `resolve_context`.\n\n```json\n" + marshalValue(value) + "\n```"
			}
		}
	case "get_skill":
		id, e := rawString(args, "skill_id", 128, true)
		if e != nil {
			return nil, -32602, e.Error(), nil
		}
		skillVersion, e := rawString(args, "version", 512, false)
		if e != nil {
			return nil, -32602, e.Error(), nil
		}
		path, e := rawString(args, "path", 1024, false)
		if e != nil {
			return nil, -32602, e.Error(), nil
		}
		skill, e := snapshot.FindSkill(id, skillVersion)
		if e != nil {
			return toolFailure(version, e.Error()), 0, "", nil
		}
		doc := skill.Document
		if path != "" {
			path, e = skillruntime.SafePath(path)
			if e != nil {
				return nil, -32602, e.Error(), nil
			}
			file, e := s.store.File(ctx, *skill, path)
			if e != nil {
				return toolFailure(version, "Package file not found"), 0, "", nil
			}
			response := map[string]any{"schemaVersion": 1, "kind": "file", "id": id, "version": doc.Version, "releaseId": skill.ReleaseID, "path": path, "checksum": file.Checksum, "mediaType": file.MediaType, "byteCount": file.ByteCount, "resourceUri": file.ResourceURI, "source": doc.Source}
			if utf8.Valid(file.Content) {
				response["text"] = string(file.Content)
				response["encoding"] = "utf-8"
			} else {
				response["blob"] = base64.StdEncoding.EncodeToString(file.Content)
				response["encoding"] = "base64"
			}
			value = response
			additional = append(additional, resourceLink(file, id, doc.Version))
			observations = append(observations, Observation{Skill: skill, Type: "supporting_file_read", Source: name})
		} else {
			files, e := s.store.Files(ctx, *skill)
			if e != nil {
				err = e
				break
			}
			descriptors := []any{}
			for _, file := range files {
				descriptors = append(descriptors, map[string]any{"path": file.Path, "checksum": file.Checksum, "mediaType": file.MediaType, "byteCount": file.ByteCount, "resourceUri": file.ResourceURI})
				additional = append(additional, resourceLink(file, id, doc.Version))
			}
			value = map[string]any{"schemaVersion": 1, "kind": "skill", "id": doc.ID, "name": doc.Name, "description": doc.Description, "instructions": doc.Instructions, "version": doc.Version, "releaseId": skill.ReleaseID, "checksum": doc.Source.Checksum, "mediaType": "text/markdown", "resourceUri": skillruntime.ResourceURI(id, "", doc.Version), "source": doc.Source, "files": descriptors}
			observations = append(observations, Observation{Skill: skill, Type: "instructions_delivered", Source: name})
		}
	case "report_skill_feedback":
		value, err = s.store.Feedback(ctx, snapshot, args)
	case "report_skill_usage":
		value, err = s.store.Usage(ctx, snapshot, p.ClientIdentity, args)
	default:
		if strings.Contains(name, ":") || !snapshot.Settings.LegacyCompiledTools {
			return nil, -32602, "Unknown tool: " + name, nil
		}
		for _, cap := range snapshot.Capabilities {
			if cap.Type == "tool" && cap.Name == name {
				skill := findCompiled(snapshot, cap.SkillID)
				if skill == nil {
					continue
				}
				textOutput = "Skill: " + skill.Name + "\nPath: " + skill.Path + "\nSummary: " + skill.Summary
				if detail, e := rawString(args, "detail", 65536, false); e == nil && detail != "" {
					textOutput += "\nDetail: " + detail
				}
				if strings.TrimSpace(skill.Body) != "" {
					textOutput += "\n\n---\n" + strings.TrimSpace(skill.Body)
					observations = append(observations, Observation{Skill: skill, Type: "instructions_delivered", Source: "compiled_tool"})
				}
				break
			}
		}
		if textOutput == "" {
			return nil, -32602, "Unknown tool: " + name, nil
		}
	}
	if err != nil {
		var inputError *InputError
		if errors.As(err, &inputError) {
			if inputError.Status == 400 {
				return nil, -32602, inputError.Message, nil
			}
			return toolFailure(version, inputError.Message), 0, "", nil
		}
		return toolFailure(version, "Internal error"), 0, "", nil
	}
	if textOutput == "" {
		textOutput = marshalValue(value)
	}
	content := []any{map[string]any{"type": "text", "text": textOutput}}
	result := map[string]any{"content": content, "isError": false}
	if rich(version) {
		if value != nil {
			result["structuredContent"] = value
		}
		result["content"] = append(content, additional...)
	}
	return result, 0, "", observations
}
func resourceLink(file skillruntime.File, id, version string) map[string]any {
	return map[string]any{"type": "resource_link", "uri": file.ResourceURI, "name": file.Path, "title": file.Path, "description": "Package file for " + id + "@" + version, "mimeType": file.MediaType, "size": file.ByteCount}
}
func toolFailure(version, message string) map[string]any {
	failure := map[string]any{"error": map[string]string{"code": "tool_failure", "message": message}}
	value := map[string]any{"content": []any{map[string]string{"type": "text", "text": marshalValue(failure)}}, "isError": true}
	if rich(version) {
		value["structuredContent"] = failure
	}
	return value
}
