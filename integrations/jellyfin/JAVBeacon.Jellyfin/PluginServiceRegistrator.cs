using Jellyfin.Plugin.JAVBeacon.Tasks;
using MediaBrowser.Controller;
using MediaBrowser.Controller.Plugins;
using MediaBrowser.Model.Tasks;
using Microsoft.Extensions.DependencyInjection;

namespace Jellyfin.Plugin.JAVBeacon;

public sealed class PluginServiceRegistrator : IPluginServiceRegistrator
{
    public void RegisterServices(IServiceCollection services, IServerApplicationHost applicationHost)
    {
        services.AddHttpClient(nameof(JAVBeaconClient));
        services.AddSingleton<JAVBeaconClient>();
        services.AddSingleton<WatchedStatusSynchronizer>();
        services.AddHostedService<PlaybackBridge>();
        services.AddHostedService<LibrarySyncService>();
        services.AddScoped<IScheduledTask, SyncWatchedStatusTask>();
    }
}
