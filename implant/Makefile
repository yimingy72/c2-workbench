CC := x86_64-w64-mingw32-gcc
CFLAGS ?= -Os -s
LDFLAGS ?= -mwindows -lws2_32 -lgdi32 -luser32 -lole32 -static

implant.exe: src/implant.c src/ops.c src/implant.h
	$(CC) $(CFLAGS) -o implant.exe src/implant.c src/ops.c $(LDFLAGS)

clean:
	rm -f implant.exe
