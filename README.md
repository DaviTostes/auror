# auror

A zero-dependency DLNA renderer written in Go that plays media using mpv.

Send media from any DLNA controller (like BubbleUPnP, smart TV or any streaming apps) directly to mpv on your machine.

## Dependencies

- [mpv](https://mpv.io/)
- Go 1.21+

## Build

```bash
go build -o auror .
```

## Usage

```bash
./auror
```

Then open any DLNA controller app on your phone or another device, look for **mpv-renderer** in the list of renderers, and cast media to it.

## How it works

`auror` announces itself on the local network via SSDP (UPnP discovery) as a `MediaRenderer` device. When a DLNA controller sends a `SetAVTransportURI` + `Play` action, auror receives the stream URL and passes it directly to mpv.

The entire implementation uses only Go's standard library — no external UPnP packages required.

## License

MIT
