# Node-RED (Docker)

Upstream install instructions, verbatim. Retrieved 2026-10-23.

Source: https://nodered.org/docs/getting-started/docker (source: github.com/node-red/node-red.github.io, docs/getting-started/docker.md, main at 5619a5a3a2)

## docker run (Quick Start)

To run in Docker in its simplest form just run:

```sh
docker run -it -p 1880:1880 -v node_red_data:/data --name mynodered nodered/node-red
```

The page says: "You can then browse to `http://{host-ip}:1880` to get the familiar Node-RED desktop."

## docker compose ("Docker Stack / Docker Compose")

```
################################################################################
# Node-RED Stack or Compose
################################################################################
# docker stack deploy node-red --compose-file docker-compose-node-red.yml
# docker-compose -f docker-compose-node-red.yml -p myNoderedProject up
################################################################################
version: "3.7"

services:
  node-red:
    image: nodered/node-red:latest
    environment:
      - TZ=Europe/Amsterdam
    ports:
      - "1880:1880"
    networks:
      - node-red-net
    volumes:
      - node-red-data:/data

volumes:
  node-red-data:

networks:
  node-red-net:
```

## Linking containers (the page's way to wire it to a broker)

You can link containers "internally" within the docker runtime by using Docker [user-defined bridges](https://docs.docker.com/network/bridge/).

Before using a bridge, it needs to be created.  The command below will create a new bridge called **iot**

    docker network create iot

Then all containers that need to communicate need to be added to the same bridge using the **--network** command line option

    docker run -itd --network iot --name mybroker eclipse-mosquitto mosquitto -c /mosquitto-no-auth.conf

(no need to expose the port 1883 globally unless you want to... as we do magic below)

Then run nodered docker, also added to the same bridge

    docker run -itd -p 1880:1880 --network iot --name mynodered nodered/node-red

containers on the same user-defined bridge can take advantage of the built in name resolution provided by the bridge and use the container name (specified using the **--name** option) as the target hostname.

In the above example the broker can be reached from the Node-RED application using hostname *mybroker*.

Then a simple flow like below show the mqtt nodes connecting to the broker

        [{"id":"c51cbf73.d90738","type":"mqtt in","z":"3fa278ec.8cbaf","name":"","topic":"test","broker":"5673f1d5.dd5f1","x":290,"y":240,"wires":[["7781c73.639b8b8"]]},{"id":"7008d6ef.b6ee38","type":"mqtt out","z":"3fa278ec.8cbaf","name":"","topic":"test","qos":"","retain":"","broker":"5673f1d5.dd5f1","x":517,"y":131,"wires":[]},{"id":"ef5b970c.7c864","type":"inject","z":"3fa278ec.8cbaf","name":"","repeat":"","crontab":"","once":false,"topic":"","payload":"","payloadType":"date","x":290,"y":153,"wires":[["7008d6ef.b6ee38"]]},{"id":"7781c73.639b8b8","type":"debug","z":"3fa278ec.8cbaf","name":"","active":true,"tosidebar":true,"console":false,"tostatus":true,"complete":"payload","targetType":"msg","statusVal":"payload","statusType":"auto","x":505,"y":257,"wires":[]},{"id":"5673f1d5.dd5f1","type":"mqtt-broker","z":"","name":"","broker":"mybroker","port":"1883","clientid":"","usetls":false,"compatmode":false,"keepalive":"15","cleansession":true,"birthTopic":"","birthQos":"0","birthRetain":"false","birthPayload":"","closeTopic":"","closeRetain":"false","closePayload":"","willTopic":"","willQos":"0","willRetain":"false","willPayload":""}]
