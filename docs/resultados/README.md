# Evidencias de ejecución

Fecha de trabajo: 24 de octubre de 2026, America/Bogota.

- `unit.json`: 35 pruebas Go, 31 Python y go vet aprobados; Go también probado en contenedor Linux Go 1.26.
- `integration.json`: 15 escenarios con la implementación final, incluido rechazo de apropiación de hash ajeno; TLS verificado, 3 CN y 3→4 DN.
- `integration-extended.json`: 16 escenarios prolongados con tiempos reales de lease y GC. Se ejecutó antes del último refuerzo de autoría, cubierto por la batería final y unitarias.
- `docker.json`: 9 comprobaciones contra Docker Compose real, Python 3.13 y seis servicios saludables.

Los JSON fueron producidos por sus programas y conservan tiempos, hashes y logs sanitizados. No se sustituyeron fallos por resultados inventados. Durante el desarrollo se corrigieron problemas de certificados estrictos, revisiones obsoletas y rebalanceo antes de registrar las ejecuciones aprobadas. Las mediciones comparten recursos del equipo y no representan un SLA ni resultados en AWS. La infraestructura cloud y la grabación del video no se ejecutaron desde este entorno.

Reproducir: `python scripts/test.py` y `python scripts/integration_test.py --extended`. Cada ejecución sobrescribe su reporte de salida; usar `--output RUTA` para conservar varias.
