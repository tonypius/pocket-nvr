#include "http_post.h"

#include <arpa/inet.h>
#include <cerrno>
#include <cstring>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <sys/socket.h>
#include <unistd.h>

namespace nvr {

std::string base64Encode(const uint8_t* data, size_t len) {
	static const char* tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
	std::string out;
	out.reserve((len + 2) / 3 * 4);
	for (size_t i = 0; i < len; i += 3) {
		uint32_t v = data[i] << 16;
		if (i + 1 < len) v |= data[i + 1] << 8;
		if (i + 2 < len) v |= data[i + 2];
		out += tbl[(v >> 18) & 63];
		out += tbl[(v >> 12) & 63];
		out += i + 1 < len ? tbl[(v >> 6) & 63] : '=';
		out += i + 2 < len ? tbl[v & 63] : '=';
	}
	return out;
}

bool httpPost(const std::string& host, int port, const std::string& path,
			  const std::string& token, const std::string& body, int timeout_sec) {
	int fd = socket(AF_INET, SOCK_STREAM, 0);
	if (fd < 0) return false;

	timeval tv{timeout_sec, 0};
	setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);
	setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof tv);

	sockaddr_in addr{};
	addr.sin_family = AF_INET;
	addr.sin_port = htons(uint16_t(port));
	if (inet_pton(AF_INET, host.c_str(), &addr.sin_addr) != 1) {
		// nvrd API is documented as 127.0.0.1; resolve "localhost" only if
		// given a name of <= 3 chars fallback — otherwise fail closed.
		if (host == "localhost") addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
		else { close(fd); return false; }
	}
	if (connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof addr) != 0) {
		fprintf(stderr, "httpPost: connect %s:%d failed errno=%d (%s)\n",
				host.c_str(), port, errno, strerror(errno));
		close(fd);
		return false;
	}

	std::string req = "POST " + path + " HTTP/1.1\r\n"
					  "Host: " + host + "\r\n"
					  "Content-Type: application/json\r\n"
					  "X-Api-Token: " + token + "\r\n"
					  "Content-Length: " + std::to_string(body.size()) + "\r\n"
					  "Connection: close\r\n\r\n";
	req += body;

	size_t sent = 0;
	while (sent < req.size()) {
		ssize_t n = ::send(fd, req.data() + sent, req.size() - sent, 0);
		if (n <= 0) {
			fprintf(stderr, "httpPost: send failed at %zu/%zu errno=%d\n",
					sent, req.size(), errno);
			close(fd);
			return false;
		}
		sent += size_t(n);
	}

	// Read status line (response may be large; we only need the code).
	char buf[128];
	ssize_t n = recv(fd, buf, sizeof buf - 1, 0);
	close(fd);
	if (n <= 0) {
		fprintf(stderr, "httpPost: recv failed errno=%d\n", errno);
		return false;
	}
	buf[n] = '\0';
	int status = 0;
	if (sscanf(buf, "HTTP/%*d.%*d %d", &status) != 1) {
		fprintf(stderr, "httpPost: unparseable response: %.40s\n", buf);
		return false;
	}
	if (status < 200 || status >= 300)
		fprintf(stderr, "httpPost: status %d from %s:%d%s\n", status, host.c_str(), port, path.c_str());
	return status >= 200 && status < 300;
}

} // namespace nvr
