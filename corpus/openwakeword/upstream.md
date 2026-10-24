# openWakeWord, as the Wyoming openWakeWord image (Docker)

Upstream install instructions, verbatim. Retrieved 2026-10-23.

Source: https://github.com/rhasspy/wyoming-openwakeword#docker-image (source: github.com/rhasspy/wyoming-openwakeword, README.md, main at 419701f64a)

## The Docker Image section, to the end of the README

From the README's "Docker Image" heading to its end, unchanged.

## Docker Image

``` sh
docker run -it -p 10400:10400 rhasspy/wyoming-openwakeword
```

### Custom Models

Download [custom wake words](https://github.com/fwartner/home-assistant-wakewords-collection) and put the `.tflite` files in a directory:

```sh
docker run -it -p 10400:10400 -v /path/to/custom/models:/custom rhasspy/wyoming-openwakeword \
    --custom-model-dir /custom
```

[Source](https://github.com/rhasspy/wyoming-addons/tree/master/openwakeword)
