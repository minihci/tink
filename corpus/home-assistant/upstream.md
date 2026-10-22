# Home Assistant (Container)

Upstream install instructions, verbatim. Retrieved 2026-10-21.

Source: https://www.home-assistant.io/installation/linux/ (source: github.com/home-assistant/home-assistant.io, source/_includes/installation/container/cli.md and container.md, current)

## docker run

```sh
docker run -d \
  --name homeassistant \
  --privileged \
  --restart=unless-stopped \
  -e TZ=MY_TIME_ZONE \
  -v /PATH_TO_YOUR_CONFIG:/config \
  -v /run/dbus:/run/dbus:ro \
  --network=host \
  ghcr.io/home-assistant/home-assistant:stable
```

(The page also says: "To give the shutdown more time, increase the timeout" with `docker run ... --stop-timeout 60 ...`.)

The docs source writes the image as a template, `{{ site.installation.container }}:{{ include.tag | default: 'stable' }}`; it is filled in above from the site config (`ghcr.io/home-assistant/home-assistant`, tag `stable`).
