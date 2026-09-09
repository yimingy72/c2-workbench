CC := x86_64-w64-mingw32-gcc
CFLAGS ?= -Os -s
LDFLAGS ?= -lws2_32 -static

implant.exe: implant.c
	$(CC) $(CFLAGS) -o implant.exe implant.c $(LDFLAGS)

clean:
	rm -f implant.exe
