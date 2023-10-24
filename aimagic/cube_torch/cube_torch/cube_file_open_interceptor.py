import os
import threading


class PrintHitCacheInfoTimer:
    def __init__(self, interval, callback):
        self.interval = interval
        self.callback = callback
        self.timer = None
        self.cancelled = False

    def _run(self):
        if self.cancelled:
            return
        self.callback()
        self.timer = threading.Timer(self.interval, self._run)
        self.timer.start()

    def start(self):
        self.timer = threading.Timer(self.interval, self._run)
        self.timer.start()

    def cancel(self):
        if self.timer is not None:
            self.cancelled = True
            self.timer.cancel()


class CubeFileOpenInterceptor:
    _instance = None
    cube_root_dir = "/tmp"  # 数据目录
    cube_cache_dir = "user memory"
    _last_cycle_hit_count = 0  # 命中次数
    _last_cycle_miss_count = 0  # 未命中次数
    total_count = 0
    total_hit_count = 0
    total_miss_count = 0
    should_exit = False
    timer = None

    def __new__(cls, *args, **kwargs):
        if not cls._instance:
            cls._instance = super().__new__(cls, *args, **kwargs)
        return cls._instance

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc_value, exc_traceback):
        pass

    @staticmethod
    def set_params(cube_root_dir):
        CubeFileOpenInterceptor.cube_root_dir = cube_root_dir
        CubeFileOpenInterceptor.timer = PrintHitCacheInfoTimer(120, CubeFileOpenInterceptor.print_hit_rate)
        CubeFileOpenInterceptor.timer.start()

    @staticmethod
    def stop_print_hitcache_timer():
        CubeFileOpenInterceptor.timer.cancel()

    @staticmethod
    def add_count(is_cache):
        if is_cache:
            CubeFileOpenInterceptor._last_cycle_hit_count += 1
        else:
            CubeFileOpenInterceptor._last_cycle_miss_count += 1

    @staticmethod
    def print_hit_rate():
        request_count = CubeFileOpenInterceptor._last_cycle_hit_count + CubeFileOpenInterceptor._last_cycle_miss_count
        if request_count == 0:
            request_count = 1

        CubeFileOpenInterceptor.total_count += request_count
        CubeFileOpenInterceptor.total_hit_count += CubeFileOpenInterceptor._last_cycle_hit_count
        CubeFileOpenInterceptor.total_miss_count += CubeFileOpenInterceptor._last_cycle_miss_count

        last_cycle_hit_rate = (CubeFileOpenInterceptor._last_cycle_hit_count / request_count) * 100
        last_cycle_miss_rate = (CubeFileOpenInterceptor._last_cycle_miss_count / request_count) * 100
        total_hit_rate = (CubeFileOpenInterceptor.total_hit_count / CubeFileOpenInterceptor.total_count) * 100
        total_miss_rate = (CubeFileOpenInterceptor.total_miss_count / CubeFileOpenInterceptor.total_count) * 100
        print_mesg = "pid:{} cube_cache_dir:{} last_cycle_metrics:([request_count:{} hit_count:{} miss_count:{} " \
                     "hit_rate:{:.2f}% miss_rate:{:.2f}% ])  sum_metrics:([request_count:{} hit_count:{} " \
                     "miss_count:{} hit_rate:{:.2f}% miss_rate:{:.2f}%]) " \
                     "".format(os.getpid(), CubeFileOpenInterceptor.cube_cache_dir, request_count,
                               CubeFileOpenInterceptor._last_cycle_hit_count,
                               CubeFileOpenInterceptor._last_cycle_miss_count,
                               last_cycle_hit_rate, last_cycle_miss_rate,
                               CubeFileOpenInterceptor.total_count,
                               CubeFileOpenInterceptor.total_hit_count,
                               CubeFileOpenInterceptor.total_miss_count,
                               total_hit_rate, total_miss_rate)

        print(print_mesg)

        CubeFileOpenInterceptor._last_cycle_hit_count = 0
        CubeFileOpenInterceptor._last_cycle_miss_count = 0
