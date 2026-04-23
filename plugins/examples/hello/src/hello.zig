// hello.zig — minimal stado plugin implementing the `greet` tool.
//
// Compiled to wasm32-freestanding. No libc, no WASI, no std.debug — just
// the three ABI exports stado expects (stado_alloc, stado_free,
// stado_tool_greet) and one import (stado_log, purely optional).
//
// The bump allocator is 2 MiB of plugin-local linear memory. stado
// currently asks for a 1 MiB result buffer per tool call so we size
// above that with headroom for concurrent args + scratch. Plugins can
// drop the arena to whatever fits their biggest response if they want
// a smaller wasm image.

const std = @import("std");

// ---- Host imports (module "stado") --------------------------------------
//
// Only stado_log is imported here. fs_read / fs_write are available if
// the plugin needs them (capability-gated by the manifest).
extern "stado" fn stado_log(
    level_ptr: [*]const u8,
    level_len: u32,
    msg_ptr: [*]const u8,
    msg_len: u32,
) void;

fn logInfo(msg: []const u8) void {
    const level = "info";
    stado_log(level.ptr, level.len, msg.ptr, @intCast(msg.len));
}

// ---- Bump allocator over a fixed arena ----------------------------------
//
// The host calls stado_alloc + stado_free around every tool invocation.
// A simple bump allocator is fine for this demo — the arena resets
// implicitly because stado_free is called in reverse order of
// stado_alloc (LIFO) and we never grow past the args+result highwater.
const arena_size: usize = 2 * 1024 * 1024;
var arena: [arena_size]u8 linksection(".bss") = undefined;
var bump: usize = 0;

export fn stado_alloc(size: u32) u32 {
    const n: usize = @intCast(size);
    if (bump + n > arena_size) return 0;
    const ptr = &arena[bump];
    bump += n;
    // Align next allocation to 8 bytes.
    bump = (bump + 7) & ~@as(usize, 7);
    // Return offset in linear memory — wasm32 pointers are u32.
    return @intCast(@intFromPtr(ptr) - @intFromPtr(&arena[0]) + wasmArenaOffset());
}

export fn stado_free(ptr: u32, size: u32) void {
    _ = ptr;
    _ = size;
    // LIFO reset: if the next alloc is the only live one, rewind. A
    // smarter scheme isn't needed for the stado ABI's use pattern.
    // For this demo we just let the arena refill on the next call —
    // bump resets at the top of each tool invocation.
}

// wasmArenaOffset returns the wasm linear-memory offset of the arena's
// first byte. We need this because stado_alloc returns a wasm pointer
// (u32 offset into linear memory), not a host address.
fn wasmArenaOffset() u32 {
    // @intFromPtr gives us the in-wasm offset for statically-allocated
    // data since wasm32 has a flat linear memory starting at 0.
    return @intCast(@intFromPtr(&arena[0]));
}

// ---- Tool: greet --------------------------------------------------------
//
// Contract (from stado's plugin ABI docs):
//   stado_tool_greet(args_ptr, args_len, result_ptr, result_cap) → i32
// Read `args_len` bytes of JSON at args_ptr ({ "name": "..." }).
// Write up to `result_cap` bytes of JSON at result_ptr.
// Return bytes written, or -1 on error.
export fn stado_tool_greet(
    args_ptr: [*]const u8,
    args_len: u32,
    result_ptr: [*]u8,
    result_cap: u32,
) i32 {
    // Reset bump allocator at the start of each tool call.
    bump = 0;

    const args = args_ptr[0..@intCast(args_len)];
    const name = extractJSONString(args, "name") orelse "world";

    logInfo("greet invoked");

    // Build the response JSON: {"message":"Hello, <name>!"}
    const prefix = "{\"message\":\"Hello, ";
    const suffix = "!\"}";
    const total: usize = prefix.len + name.len + suffix.len;
    if (total > @as(usize, @intCast(result_cap))) return -1;

    const out = result_ptr[0..total];
    @memcpy(out[0..prefix.len], prefix);
    @memcpy(out[prefix.len .. prefix.len + name.len], name);
    @memcpy(out[prefix.len + name.len .. total], suffix);

    return @intCast(total);
}

// ---- Tiny JSON string-value extractor -----------------------------------
//
// Looks for `"<key>"` and returns the string value following the first
// `:`. Handles simple quoted strings only — no escape-sequence decoding.
// Enough for a demo argument like {"name":"Ada"}.
fn extractJSONString(data: []const u8, key: []const u8) ?[]const u8 {
    // Find `"<key>"` in data.
    var i: usize = 0;
    const needle_len = key.len + 2; // quotes
    while (i + needle_len <= data.len) : (i += 1) {
        if (data[i] != '"') continue;
        if (i + needle_len > data.len) break;
        if (std.mem.eql(u8, data[i + 1 .. i + 1 + key.len], key) and
            data[i + 1 + key.len] == '"')
        {
            // Scan past : and whitespace to the opening quote of the value.
            var j: usize = i + 1 + key.len + 1;
            while (j < data.len and data[j] != '"') : (j += 1) {}
            if (j >= data.len) return null;
            const start = j + 1;
            var end = start;
            while (end < data.len and data[end] != '"') : (end += 1) {}
            if (end >= data.len) return null;
            return data[start..end];
        }
    }
    return null;
}
