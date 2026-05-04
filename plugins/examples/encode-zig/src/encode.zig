// encode.zig — stado plugin: encode/decode data between common formats.
//
// Tools:
//   encode {data, format, direction?}
//     → {result, format, direction}
//
// Formats: base64, base64url, hex, url (percent-encode), url-decode,
//          html-entities.
//
// Direction: "encode" (default) or "decode" (not all formats support decode).
//
// Compiled to wasm32-freestanding, ~3KB. Contrast with Go equivalent at ~3.5MB.
// This is the Zig SDK proof for stado's polyglot plugin runtime.
//
// No libc, no WASI, no std — only the stado ABI and a bump allocator.

const std = @import("std");

// ---- Host imports -------------------------------------------------------

extern "stado" fn stado_log(
    level_ptr: [*]const u8,
    level_len: u32,
    msg_ptr: [*]const u8,
    msg_len: u32,
) void;

fn logInfo(msg: []const u8) void {
    stado_log("info".ptr, 4, msg.ptr, @intCast(msg.len));
}

// ---- Bump allocator -----------------------------------------------------

const arena_size: usize = 2 * 1024 * 1024; // 2 MiB: host uses separate stado_alloc calls for args + result
var arena: [arena_size]u8 linksection(".bss") = undefined;
var bump: usize = 0;

export fn stado_alloc(size: u32) u32 {
    const n: usize = @intCast(size);
    if (bump + n > arena_size) return 0;
    const ptr = &arena[bump];
    bump += n;
    bump = (bump + 7) & ~@as(usize, 7);
    return @intCast(@intFromPtr(ptr));
}

export fn stado_free(_: u32, _: u32) void {}

// ---- JSON helpers -------------------------------------------------------

fn jsonStr(data: []const u8, key: []const u8) ?[]const u8 {
    var i: usize = 0;
    while (i + key.len + 2 < data.len) : (i += 1) {
        if (data[i] != '"') continue;
        if (i + 1 + key.len >= data.len) break;
        if (!std.mem.eql(u8, data[i + 1 .. i + 1 + key.len], key)) continue;
        if (i + 1 + key.len >= data.len or data[i + 1 + key.len] != '"') continue;
        var j = i + 1 + key.len + 1;
        while (j < data.len and data[j] != '"') : (j += 1) {}
        if (j >= data.len) return null;
        const start = j + 1;
        var end = start;
        // Handle basic JSON string escaping — we just want to find the close quote.
        while (end < data.len) : (end += 1) {
            if (data[end] == '\\') { end += 1; continue; }
            if (data[end] == '"') break;
        }
        if (end >= data.len) return null;
        return data[start..end];
    }
    return null;
}

fn writeResult(result_ptr: [*]u8, result_cap: u32, encoded: []const u8, format: []const u8, dir: []const u8) i32 {
    // {"result":"...","format":"...","direction":"..."}
    const pre1 = "{\"result\":\"";
    const pre2 = "\",\"format\":\"";
    const pre3 = "\",\"direction\":\"";
    const suf = "\"}";
    const total = pre1.len + encoded.len + pre2.len + format.len + pre3.len + dir.len + suf.len;
    if (total > @as(usize, @intCast(result_cap))) return -1;
    var out = result_ptr[0..total];
    var off: usize = 0;
    @memcpy(out[off .. off + pre1.len], pre1); off += pre1.len;
    @memcpy(out[off .. off + encoded.len], encoded); off += encoded.len;
    @memcpy(out[off .. off + pre2.len], pre2); off += pre2.len;
    @memcpy(out[off .. off + format.len], format); off += format.len;
    @memcpy(out[off .. off + pre3.len], pre3); off += pre3.len;
    @memcpy(out[off .. off + dir.len], dir); off += dir.len;
    @memcpy(out[off .. off + suf.len], suf);
    return @intCast(total);
}

fn writeError(result_ptr: [*]u8, result_cap: u32, msg: []const u8) i32 {
    const pre = "{\"error\":\"";
    const suf = "\"}";
    const total = pre.len + msg.len + suf.len;
    if (total > @as(usize, @intCast(result_cap))) return -1;
    var out = result_ptr[0..total];
    @memcpy(out[0..pre.len], pre);
    @memcpy(out[pre.len .. pre.len + msg.len], msg);
    @memcpy(out[pre.len + msg.len .. total], suf);
    return @intCast(total);
}

// ---- Base64 (standard + URL-safe) ----------------------------------------

const b64chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
const b64urlchars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";

fn b64enc(src: []const u8, out: []u8, alphabet: []const u8) usize {
    var si: usize = 0;
    var di: usize = 0;
    while (si + 3 <= src.len) : ({ si += 3; }) {
        const b0 = src[si]; const b1 = src[si+1]; const b2 = src[si+2];
        out[di]   = alphabet[(b0 >> 2) & 0x3F];
        out[di+1] = alphabet[((b0 & 0x03) << 4) | (b1 >> 4)];
        out[di+2] = alphabet[((b1 & 0x0F) << 2) | (b2 >> 6)];
        out[di+3] = alphabet[b2 & 0x3F];
        di += 4;
    }
    const rem = src.len - si;
    if (rem == 1) {
        out[di]   = alphabet[(src[si] >> 2) & 0x3F];
        out[di+1] = alphabet[(src[si] & 0x03) << 4];
        out[di+2] = '='; out[di+3] = '='; di += 4;
    } else if (rem == 2) {
        out[di]   = alphabet[(src[si] >> 2) & 0x3F];
        out[di+1] = alphabet[((src[si] & 0x03) << 4) | (src[si+1] >> 4)];
        out[di+2] = alphabet[(src[si+1] & 0x0F) << 2];
        out[di+3] = '='; di += 4;
    }
    return di;
}

fn b64dec(src: []const u8, out: []u8, urlsafe: bool) ?usize {
    const table = blk: {
        var t: [256]u8 = [_]u8{255} ** 256;
        for (b64chars, 0..) |c, i| t[c] = @intCast(i);
        if (urlsafe) { t['-'] = 62; t['_'] = 63; } else { t['+'] = 62; t['/'] = 63; }
        t['='] = 0;
        break :blk t;
    };
    var si: usize = 0;
    var di: usize = 0;
    // Trim trailing =
    while (si + 4 <= src.len) : ({ si += 4; }) {
        const s = src[si..si+4];
        const v0 = table[s[0]]; const v1 = table[s[1]];
        const v2 = table[s[2]]; const v3 = table[s[3]];
        if (v0 == 255 or v1 == 255 or v2 == 255 or v3 == 255) return null;
        out[di]   = (v0 << 2) | (v1 >> 4); di += 1;
        if (s[2] != '=') { out[di] = ((v1 & 0xF) << 4) | (v2 >> 2); di += 1; }
        if (s[3] != '=') { out[di] = ((v2 & 0x3) << 6) | v3; di += 1; }
    }
    return di;
}

// ---- Hex -----------------------------------------------------------------

const hexchars = "0123456789abcdef";

fn hexenc(src: []const u8, out: []u8) usize {
    for (src, 0..) |b, i| {
        out[i*2]   = hexchars[(b >> 4) & 0xF];
        out[i*2+1] = hexchars[b & 0xF];
    }
    return src.len * 2;
}

fn hexDig(c: u8) ?u8 {
    return switch (c) {
        '0'...'9' => c - '0',
        'a'...'f' => c - 'a' + 10,
        'A'...'F' => c - 'A' + 10,
        else => null,
    };
}

fn hexdec(src: []const u8, out: []u8) ?usize {
    if (src.len % 2 != 0) return null;
    var i: usize = 0;
    while (i < src.len) : (i += 2) {
        const hi = hexDig(src[i]) orelse return null;
        const lo = hexDig(src[i+1]) orelse return null;
        out[i/2] = (hi << 4) | lo;
    }
    return src.len / 2;
}

// ---- URL percent-encode --------------------------------------------------

fn isUnreserved(c: u8) bool {
    return (c >= 'A' and c <= 'Z') or (c >= 'a' and c <= 'z') or
           (c >= '0' and c <= '9') or c == '-' or c == '_' or c == '.' or c == '~';
}

fn urlenc(src: []const u8, out: []u8) usize {
    var di: usize = 0;
    for (src) |b| {
        if (isUnreserved(b)) {
            out[di] = b; di += 1;
        } else {
            out[di] = '%'; out[di+1] = hexchars[(b >> 4) & 0xF]; out[di+2] = hexchars[b & 0xF];
            di += 3;
        }
    }
    return di;
}

fn urlDec(src: []const u8, out: []u8) ?usize {
    var si: usize = 0;
    var di: usize = 0;
    while (si < src.len) {
        if (src[si] == '+') { out[di] = ' '; si += 1; di += 1; }
        else if (src[si] == '%' and si + 2 < src.len) {
            const hi = hexDig(src[si+1]) orelse return null;
            const lo = hexDig(src[si+2]) orelse return null;
            out[di] = (hi << 4) | lo;
            si += 3; di += 1;
        } else {
            out[di] = src[si]; si += 1; di += 1;
        }
    }
    return di;
}

// ---- HTML entity encode --------------------------------------------------

fn htmlenc(src: []const u8, out: []u8) usize {
    var di: usize = 0;
    for (src) |c| {
        switch (c) {
            '&' => { @memcpy(out[di..di+5], "&amp;"); di += 5; },
            '<' => { @memcpy(out[di..di+4], "&lt;"); di += 4; },
            '>' => { @memcpy(out[di..di+4], "&gt;"); di += 4; },
            '"' => { @memcpy(out[di..di+6], "&quot;"); di += 6; },
            '\'' => { @memcpy(out[di..di+6], "&#39;"); di += 5; },
            else => { out[di] = c; di += 1; },
        }
    }
    return di;
}

// ---- Tool: encode --------------------------------------------------------

export fn stado_tool_encode(
    args_ptr: [*]const u8,
    args_len: u32,
    result_ptr: [*]u8,
    result_cap: u32,
) i32 {
    bump = 0; // reset allocator each call

    const args = args_ptr[0..@intCast(args_len)];
    const data = jsonStr(args, "data") orelse
        return writeError(result_ptr, result_cap, "data is required");
    const fmt_raw = jsonStr(args, "format") orelse
        return writeError(result_ptr, result_cap, "format is required (base64, base64url, hex, url, html)");
    const dir_raw = jsonStr(args, "direction") orelse "encode";

    const decode_mode = std.mem.eql(u8, dir_raw, "decode");

    // Scratch buffer — 3x data len is enough for worst-case URL-encoding.
    const scratch_len = data.len * 3 + 16;
    if (bump + scratch_len > arena_size) return -1;
    var scratch = arena[bump .. bump + scratch_len];
    bump += scratch_len;

    var encoded_len: usize = 0;

    if (std.mem.eql(u8, fmt_raw, "base64")) {
        if (decode_mode) {
            encoded_len = b64dec(data, scratch, false) orelse
                return writeError(result_ptr, result_cap, "invalid base64 input");
        } else {
            encoded_len = b64enc(data, scratch, b64chars);
        }
    } else if (std.mem.eql(u8, fmt_raw, "base64url")) {
        if (decode_mode) {
            encoded_len = b64dec(data, scratch, true) orelse
                return writeError(result_ptr, result_cap, "invalid base64url input");
        } else {
            encoded_len = b64enc(data, scratch, b64urlchars);
        }
    } else if (std.mem.eql(u8, fmt_raw, "hex")) {
        if (decode_mode) {
            encoded_len = hexdec(data, scratch) orelse
                return writeError(result_ptr, result_cap, "invalid hex input (must be even-length hex string)");
        } else {
            encoded_len = hexenc(data, scratch);
        }
    } else if (std.mem.eql(u8, fmt_raw, "url")) {
        if (decode_mode) {
            encoded_len = urlDec(data, scratch) orelse
                return writeError(result_ptr, result_cap, "invalid URL-encoded input");
        } else {
            encoded_len = urlenc(data, scratch);
        }
    } else if (std.mem.eql(u8, fmt_raw, "html")) {
        if (decode_mode) {
            return writeError(result_ptr, result_cap, "html decode not supported — use url or base64 for reversible encoding");
        }
        encoded_len = htmlenc(data, scratch);
    } else {
        return writeError(result_ptr, result_cap, "format must be: base64, base64url, hex, url, html");
    }

    logInfo("encode invoked");
    return writeResult(result_ptr, result_cap, scratch[0..encoded_len], fmt_raw, dir_raw);
}
