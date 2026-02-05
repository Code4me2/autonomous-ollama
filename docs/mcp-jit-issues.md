# MCP JIT Implementation - Remaining Issues

## Status: Work in Progress

This document tracks known issues with the unified MCP/JIT architecture as of 2026-02-02.

---

## Issue 1: Parser Inconsistency on First Request

**Symptom:** First tool call sometimes outputs raw format instead of being parsed:
```
mcp_discover[ARGS]{"pattern": "*file*"}
```

**Expected:** Tool should execute, showing:
```
🔧 Executing tool 'mcp_discover'
   Arguments: pattern: *file*
```

**Root Cause:** Model may not output `[TOOL_CALLS]` prefix consistently. The ministral parser requires:
```
[TOOL_CALLS]toolname[ARGS]{"arg": "value"}
```

**Observations from logs:**
- `"No tools called, conversation complete" round=0 content_length=39`
- Model generated 39 chars of content with no detected tool call
- Second attempt ("try once more") worked correctly

**Potential Fixes:**
1. Make parser more lenient (detect `toolname[ARGS]` without prefix)
2. Improve system prompt to reinforce correct format
3. Add format examples to JIT context injection

---

## Issue 2: Tool Discovery Limit Too Restrictive

**Symptom:** Essential tools like `list_directory` and `search_files` are not discovered.

**Current Behavior:**
- `max_tools_per_discovery = 5` (default)
- Filesystem server has **14 tools** total
- Discovery returns first 5 matching tools alphabetically
- `list_directory` gets cut off

**Discovered tools (5):**
- `filesystem:read_file`
- `filesystem:read_text_file`
- `filesystem:read_media_file`
- `filesystem:read_multiple_files`
- `filesystem:write_file`

**Missing critical tools:**
- `filesystem:list_directory` - needed to list directory contents
- `filesystem:search_files` - needed to find files
- `filesystem:get_file_info` - needed for file metadata

**Result:** Model tries to use `read_multiple_files` to list a directory, which fails.

**Potential Fixes:**
1. Increase default limit to 10-15
2. Prioritize "essential" tools (list, search) in discovery
3. Allow multiple discovery calls with different patterns
4. Sort tools by relevance/usefulness rather than alphabetically

---

## Issue 3: Model Uses Wrong Tools for Task

**Symptom:** Model attempts directory listing with file read tools.

**Log evidence:**
```
Tool call details round=1 name=filesystem:read_multiple_files arguments={paths: [/home/velvetm]}
Result: EACCES: permission denied, open '/home/velvetm'

Tool call details round=2 name=filesystem:read_multiple_files arguments={paths: [/home/velvetm/*]}
Result: ENOENT: no such file or directory, open '/home/velvetm/*'
```

**Root Cause:**
- `list_directory` tool not in discovered set
- Model doesn't know to do second discovery with `*list*` pattern
- Model improvises with available tools

**Potential Fixes:**
1. Include `list_directory` in initial discovery (see Issue 2)
2. Add guidance in JIT context: "If you need to list files, discover with `*list*` pattern"
3. Implement tool suggestions based on task type

---

## Issue 4: Raw Tool Call Format Leaking to Output

**Symptom:** After tool execution, raw format appears in response:
```
✅ Result:
Found 5 tools matching '*file*':
...

mcp_discover[ARGS]{"pattern": "*file*"}   <-- This shouldn't appear
```

**Root Cause:** Model continues generating after tool call, echoing the format as "explanation."

**Parser behavior:**
1. Parser extracts `[TOOL_CALLS]mcp_discover[ARGS]{...}` as tool call
2. Transitions back to content collection state
3. Model generates more text including the format
4. This passes through as content

**Potential Fixes:**
1. Filter output matching `toolname[ARGS]{...}` pattern
2. Stop generation after tool call detected
3. Post-process response to remove tool call echoes

---

## Issue 5: No Automatic Multi-Pattern Discovery

**Symptom:** User asks to "list files and read them" but model only discovers read tools.

**Current Flow:**
1. Model calls `mcp_discover("*file*")`
2. Gets read/write tools
3. Doesn't realize it needs `list_directory`
4. Fails to complete task

**Ideal Flow:**
1. Model analyzes task: needs list + read
2. Calls `mcp_discover("*list*")` → gets `list_directory`
3. Calls `mcp_discover("*file*")` → gets read/write tools
4. Uses both to complete task

**Potential Fixes:**
1. Smarter system prompt guiding multi-pattern discovery
2. Automatic "essential tools" pre-discovery
3. Task analysis before discovery

---

## Configuration Reference

Current defaults in `mcp_manager.go`:
```go
maxToolsPerDiscovery = 5  // Consider increasing to 10-15
maxClients = 10
```

JIT context injection in `mcp_code_api.go`:
```go
// Current prompt may need enhancement for format consistency
context.WriteString(`You have access to external tools via MCP...`)
```

---

## Testing Checklist

- [ ] First request parses tool call correctly
- [ ] Discovery returns essential tools (list, search, read, write)
- [ ] Model uses correct tools for directory listing
- [ ] No raw tool format in output
- [ ] Multi-round tool usage works
- [ ] Discovery results persist across rounds

---

## Priority Order

1. **High:** Issue 2 (tool limit) - blocking basic functionality
2. **High:** Issue 3 (wrong tools) - consequence of Issue 2
3. **Medium:** Issue 1 (parser inconsistency) - intermittent
4. **Medium:** Issue 4 (format leaking) - cosmetic but confusing
5. **Low:** Issue 5 (multi-pattern) - enhancement
