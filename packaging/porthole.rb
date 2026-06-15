class Porthole < Formula
  desc "Local web console for Apple's container runtime (macOS, Apple silicon)"
  homepage "https://github.com/<you>/porthole"
  url "https://github.com/<you>/porthole/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "REPLACE_WITH_TARBALL_SHA256"
  license "MIT"

  depends_on "go" => :build
  depends_on "node" => :build
  depends_on :macos
  depends_on arch: :arm64

  def install
    # Build the embedded SPA, then compile the single binary with the version
    # stamped from the formula (matches the Makefile's ldflags).
    system "npm", "--prefix", "web", "ci"
    system "npm", "--prefix", "web", "run", "build"
    system "go", "build", "-ldflags", "-X main.version=#{version}", "-o", bin/"portholed", "./cmd/portholed"
  end

  service do
    # Runs as the user (brew services uses a LaunchAgent), so portholed can shell
    # the per-user `container` CLI and write its store under ~/Library.
    run [opt_bin/"portholed", "-container-bin", "/usr/local/bin/container"]
    keep_alive true
    log_path var/"log/porthole/portholed.log"
    error_log_path var/"log/porthole/portholed.log"
  end

  test do
    assert_match "porthole", shell_output("#{bin}/portholed -version")
  end
end
