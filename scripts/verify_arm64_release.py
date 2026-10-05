import ctypes
import json
import os
import subprocess
import tempfile
import time
import urllib.request


class Buffer(ctypes.Structure):
    _fields_ = [("ptr", ctypes.c_void_p), ("length", ctypes.c_size_t)]


Call = ctypes.CFUNCTYPE(ctypes.c_int, ctypes.c_char_p, ctypes.c_char_p, ctypes.c_size_t, ctypes.POINTER(Buffer))
Free = ctypes.CFUNCTYPE(None, ctypes.c_void_p, ctypes.c_size_t)
Shutdown = ctypes.CFUNCTYPE(None)


class PluginAPI(ctypes.Structure):
    _fields_ = [("version", ctypes.c_uint32), ("call", Call), ("free", Free), ("shutdown", Shutdown)]


plugin = ctypes.CDLL("./dist/cpa-session-archive.so")
plugin.cliproxy_plugin_init.argtypes = [ctypes.c_void_p, ctypes.POINTER(PluginAPI)]
plugin.cliproxy_plugin_init.restype = ctypes.c_int
api = PluginAPI()
assert plugin.cliproxy_plugin_init(None, ctypes.byref(api)) == 0
assert api.version == 1
out = Buffer()
assert api.call(b"management.register", b"{}", 2, ctypes.byref(out)) == 0
try:
    assert json.loads(ctypes.string_at(out.ptr, out.length))["ok"] is True
finally:
    api.free(out.ptr, out.length)
    api.shutdown()

with tempfile.TemporaryDirectory() as directory:
    env = dict(os.environ, ARCHIVE_DB=directory + "/archive.sqlite", LISTEN_ADDR="127.0.0.1:18080")
    collector = subprocess.Popen(["./dist/cpa-session-collector-linux-arm64"], env=env)
    try:
        for attempt in range(100):
            assert collector.poll() is None, "collector exited during startup"
            try:
                with urllib.request.urlopen("http://127.0.0.1:18080/healthz", timeout=1) as response:
                    assert response.read() == b"ok"
                break
            except OSError:
                time.sleep(0.1)
        else:
            raise AssertionError("collector did not become healthy")
    finally:
        collector.terminate()
        collector.wait(timeout=10)
print("ARM64 native plugin ABI and collector SQLite health verified")
