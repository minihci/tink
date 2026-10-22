# n8n

Upstream install instructions, verbatim. Retrieved 2026-10-21.

Source: https://github.com/n8n-io/n8n (README.md, master)

## docker run

```sh
docker volume create n8n_data
docker run -it --rm --name n8n -p 5678:5678 -v n8n_data:/home/node/.n8n docker.n8n.io/n8nio/n8n
```
