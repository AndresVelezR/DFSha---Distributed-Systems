FROM python:3.13-alpine
WORKDIR /app
COPY client/dfsha.py /usr/local/bin/dfsha
RUN chmod +x /usr/local/bin/dfsha
ENTRYPOINT ["dfsha"]
