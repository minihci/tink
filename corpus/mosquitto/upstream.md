# Eclipse Mosquitto

Upstream install instructions, verbatim. Retrieved 2026-10-21.

Source: https://github.com/docker-library/docs (eclipse-mosquitto/README.md, master)

## docker run, config directory only

```sh
$ docker run -it -p 1883:1883 -v "$PWD/mosquitto/config:/mosquitto/config" eclipse-mosquitto
```

## docker run, with persistent data and log directories

```sh
$ docker run -it -p 1883:1883 -v "$PWD/mosquitto/config:/mosquitto/config" -v "$PWD/mosquitto/data:/mosquitto/data" -v "$PWD/mosquitto/log:/mosquitto/log" eclipse-mosquitto
```
