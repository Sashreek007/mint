# nginx with our config baked in (no bind mount).
# Bind-mounting a single config file triggers a macOS Docker Desktop
# file-sharing deadlock (EDEADLK on pread of /etc/nginx/nginx.conf);
# COPYing it into the image sidesteps that class of bug entirely.
FROM nginx:1.27-alpine
COPY nginx.conf /etc/nginx/nginx.conf
