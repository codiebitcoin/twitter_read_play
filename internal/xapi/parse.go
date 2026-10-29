package xapi

// 把 GraphQL 响应收成与 chrome 脚本相同的 v1.1 形状（id_str / full_text / user.legacy）。

func normalizeUser(res map[string]any) map[string]any {
	if res == nil {
		return nil
	}
	if tw, ok := res["tweet"].(map[string]any); ok && res["legacy"] == nil {
		res = tw
	}
	legacy, _ := res["legacy"].(map[string]any)
	core, _ := res["core"].(map[string]any)
	out := map[string]any{}
	for k, v := range legacy {
		out[k] = v
	}
	if str(out["id_str"]) == "" {
		out["id_str"] = firstStr(res["rest_id"], res["id"], legacy["id_str"])
	}
	if str(out["screen_name"]) == "" {
		out["screen_name"] = firstStr(core["screen_name"], legacy["screen_name"])
	}
	if str(out["name"]) == "" {
		out["name"] = firstStr(core["name"], legacy["name"])
	}
	if str(out["created_at"]) == "" {
		out["created_at"] = firstStr(core["created_at"], legacy["created_at"])
	}
	if _, ok := out["description"]; !ok {
		if bio, ok := res["profile_bio"].(map[string]any); ok {
			out["description"] = str(bio["description"])
		}
	}
	if _, ok := out["followers_count"]; !ok {
		if rc, ok := res["relationship_counts"].(map[string]any); ok {
			out["followers_count"] = rc["followers"]
		}
	}
	if _, ok := out["friends_count"]; !ok {
		if rc, ok := res["relationship_counts"].(map[string]any); ok {
			out["friends_count"] = rc["following"]
		}
	}
	if _, ok := out["statuses_count"]; !ok {
		if tc, ok := res["tweet_counts"].(map[string]any); ok {
			out["statuses_count"] = tc["tweets"]
		}
	}
	if _, ok := out["profile_image_url_https"]; !ok {
		if av, ok := res["avatar"].(map[string]any); ok {
			out["profile_image_url_https"] = str(av["image_url"])
		}
	}
	if _, ok := out["verified"]; !ok {
		out["verified"] = truthy(res["is_blue_verified"])
	}
	return out
}

func extractTweets(doc map[string]any) ([]map[string]any, string) {
	data, _ := doc["data"].(map[string]any)
	user, _ := data["user"].(map[string]any)
	result, _ := user["result"].(map[string]any)
	timeline, _ := result["timeline"].(map[string]any)
	inner, _ := timeline["timeline"].(map[string]any)
	inst, _ := inner["instructions"].([]any)
	var out []map[string]any
	seen := map[string]bool{}
	cursor := ""
	for _, raw := range inst {
		ins, _ := raw.(map[string]any)
		var entries []any
		if es, ok := ins["entries"].([]any); ok {
			entries = es
		} else if e, ok := ins["entry"].(map[string]any); ok {
			entries = []any{e}
		}
		for _, er := range entries {
			e, _ := er.(map[string]any)
			content, _ := e["content"].(map[string]any)
			if content != nil && str(content["cursorType"]) == "Bottom" {
				if v := str(content["value"]); v != "" {
					cursor = v
				}
			}
			items := itemContents(content, e)
			for _, it := range items {
				if str(it["itemType"]) != "TimelineTweet" {
					continue
				}
				tr, _ := it["tweet_results"].(map[string]any)
				res, _ := tr["result"].(map[string]any)
				legacy, _ := res["legacy"].(map[string]any)
				if legacy == nil {
					if tw, ok := res["tweet"].(map[string]any); ok {
						res = tw
						legacy, _ = tw["legacy"].(map[string]any)
					}
				}
				if legacy == nil || str(legacy["id_str"]) == "" || seen[str(legacy["id_str"])] {
					continue
				}
				seen[str(legacy["id_str"])] = true
				obj := map[string]any{}
				for k, v := range legacy {
					obj[k] = v
				}
				obj["user"] = userOfTweet(res)
				if q := nestedResult(res, "quoted_status_result"); q != nil {
					if ql, _ := q["legacy"].(map[string]any); ql != nil {
						qq := map[string]any{}
						for k, v := range ql {
							qq[k] = v
						}
						qq["user"] = userOfTweet(q)
						obj["quoted_status"] = qq
					}
				}
				if rt := nestedResult(res, "retweeted_status_result"); rt != nil {
					if rl, _ := rt["legacy"].(map[string]any); rl != nil {
						rr := map[string]any{}
						for k, v := range rl {
							rr[k] = v
						}
						rr["user"] = userOfTweet(rt)
						obj["retweeted_status"] = rr
					}
				}
				out = append(out, obj)
			}
		}
	}
	return out, cursor
}

func itemContents(content, entry map[string]any) []map[string]any {
	var items []map[string]any
	if content == nil {
		return items
	}
	switch str(content["entryType"]) {
	case "TimelineTimelineItem":
		if ic, ok := content["itemContent"].(map[string]any); ok {
			items = append(items, ic)
		}
	case "TimelineTimelineModule":
		if arr, ok := content["items"].([]any); ok {
			for _, raw := range arr {
				m, _ := raw.(map[string]any)
				item, _ := m["item"].(map[string]any)
				if ic, ok := item["itemContent"].(map[string]any); ok {
					items = append(items, ic)
				}
			}
		}
	}
	if ic, ok := entry["itemContent"].(map[string]any); ok {
		items = append(items, ic)
	}
	return items
}

func userOfTweet(res map[string]any) map[string]any {
	core, _ := res["core"].(map[string]any)
	ur, _ := core["user_results"].(map[string]any)
	u, _ := ur["result"].(map[string]any)
	if n := normalizeUser(u); n != nil {
		return n
	}
	return map[string]any{}
}

func nestedResult(res map[string]any, key string) map[string]any {
	box, _ := res[key].(map[string]any)
	r, _ := box["result"].(map[string]any)
	return r
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func firstStr(vs ...any) string {
	for _, v := range vs {
		if s := str(v); s != "" {
			return s
		}
	}
	return ""
}

func truthy(v any) bool {
	b, _ := v.(bool)
	return b
}

func ltID(a, b string) bool {
	if a == b || b == "" {
		return false
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}
