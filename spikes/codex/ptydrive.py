# drive an interactive CLI in a PTY: ptydrive.py <log> <steps-json> -- cmd...
import os, pty, sys, time, json, select
log, steps = sys.argv[1], json.loads(sys.argv[2])
cmd = sys.argv[sys.argv.index('--')+1:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
import fcntl, termios, struct
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', 40, 120, 0, 0))
out = open(log, 'wb')
def pump(sec):
    end = time.time() + sec
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.2)
        if r:
            try:
                b = os.read(fd, 65536)
            except OSError:
                return False
            if not b: return False
            out.write(b); out.flush()
            # answer terminal queries minimally (cursor position)
            if b'\x1b[6n' in b: os.write(fd, b'\x1b[1;1R')
    return True
for st in steps:
    if 'wait' in st: pump(st['wait'])
    if 'type' in st:
        os.write(fd, st['type'].encode()); pump(0.8); os.write(fd, b'\r')
    if 'key' in st: os.write(fd, st['key'].encode())
pump(2)
try: os.kill(pid, 9)
except Exception: pass
