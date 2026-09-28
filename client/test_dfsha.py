"""Pruebas del cliente. Ejecutar desde la raíz: python3 -m unittest discover client"""
import os
import tempfile
import unittest
import urllib.error
from unittest import mock

import dfsha


class NetworkRetriesTest(unittest.TestCase):
    def setUp(self):
        patcher = mock.patch("dfsha.time.sleep")
        self.sleep = patcher.start()
        self.addCleanup(patcher.stop)
        stderr = mock.patch("sys.stderr")
        stderr.start()
        self.addCleanup(stderr.stop)

    def test_retries_network_failures_with_growing_waits(self):
        action = mock.Mock(side_effect=[urllib.error.URLError("caído"), ConnectionResetError(), "ok"])
        self.assertEqual(dfsha.with_network_retries(action, "bloque 0"), "ok")
        self.assertEqual(action.call_count, 3)
        self.assertEqual([c.args[0] for c in self.sleep.call_args_list], [1, 2])

    def test_gives_up_after_three_retries(self):
        action = mock.Mock(side_effect=TimeoutError())
        with self.assertRaises(TimeoutError):
            dfsha.with_network_retries(action, "bloque 0")
        self.assertEqual(action.call_count, 4)  # intento inicial + 3 reintentos
        self.assertEqual([c.args[0] for c in self.sleep.call_args_list], [1, 2, 4])

    def test_does_not_retry_server_errors(self):
        action = mock.Mock(side_effect=dfsha.DfshaHttpError(400, "petición inválida"))
        with self.assertRaises(dfsha.DfshaHttpError):
            dfsha.with_network_retries(action, "bloque 0")
        self.assertEqual(action.call_count, 1)
        self.sleep.assert_not_called()


class ExpiredReservationTest(unittest.TestCase):
    def setUp(self):
        stderr = mock.patch("sys.stderr")
        stderr.start()
        self.addCleanup(stderr.stop)
        self.client = dfsha.DfshaClient("http://controlnode:8000")

    def test_requests_a_new_plan_once_when_commit_returns_410(self):
        with mock.patch.object(self.client, "_put_once", side_effect=[dfsha.DfshaHttpError(410, "reserva vencida"), None]) as put_once:
            self.client.put("local.bin", "/remoto.bin")
        self.assertEqual(put_once.call_count, 2)

    def test_other_server_errors_are_not_retried(self):
        with mock.patch.object(self.client, "_put_once", side_effect=dfsha.DfshaHttpError(409, "reserva activa")) as put_once:
            with self.assertRaises(dfsha.DfshaHttpError):
                self.client.put("local.bin", "/remoto.bin")
        self.assertEqual(put_once.call_count, 1)


class BlockFileIoTest(unittest.TestCase):
    def test_blocks_written_out_of_order_rebuild_the_file(self):
        original = os.urandom(25)
        block_size = 10
        with tempfile.TemporaryDirectory() as tmp:
            source = os.path.join(tmp, "origen.bin")
            target = os.path.join(tmp, "destino.bin")
            with open(source, "wb") as f:
                f.write(original)
            with open(target, "wb") as f:
                f.truncate(len(original))

            for index in (2, 0, 1):  # llegan en desorden, como en la descarga paralela
                data = dfsha.read_block(source, index, block_size)
                dfsha.write_block(target, index, block_size, data)

            with open(target, "rb") as f:
                self.assertEqual(f.read(), original)


if __name__ == "__main__":
    unittest.main()
