FROM python:3.13-alpine
WORKDIR /work
COPY client/dfsha.py /usr/local/bin/dfsha
# /session guarda el token entre comandos. Se deja escribible para cualquier
# usuario porque Compose ejecuta el cliente con el usuario de la máquina.
RUN chmod +x /usr/local/bin/dfsha && mkdir -p /session && chmod 1777 /session
ENV PYTHONUNBUFFERED=1 DFSHA_SESSION=/session/session.json DFSHA_CA=/secrets/ca.crt
ENTRYPOINT ["python", "/usr/local/bin/dfsha"]
