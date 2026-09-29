// Tiny utilities for emitting events to nvrd's localhost API: base64 and
// a dependency-free HTTP/1.1 POST over POSIX sockets (bionic-compatible).
#pragma once

#include <cstdint>
#include <string>
#include <vector>

namespace nvr {

std::string base64Encode(const uint8_t* data, size_t len);

// POSTs body with X-Api-Token; returns true on 2xx. Never throws.
bool httpPost(const std::string& host, int port, const std::string& path,
			  const std::string& token, const std::string& body,
			  int timeout_sec = 5);

} // namespace nvr
